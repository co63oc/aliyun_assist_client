package acspluginmanager

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rodaine/table"

	. "github.com/aliyun/aliyun_assist_client/agent/pluginmanager"
	"github.com/aliyun/aliyun_assist_client/agent/log"
	"github.com/aliyun/aliyun_assist_client/agent/pluginmanager/acspluginmanager/thirdparty/shlex"
	"github.com/aliyun/aliyun_assist_client/agent/util"
	"github.com/aliyun/aliyun_assist_client/agent/util/osutil"
	"github.com/aliyun/aliyun_assist_client/agent/util/process"
	"github.com/aliyun/aliyun_assist_client/agent/util/versionutil"
)

type pluginConfig struct {
	Name              string      `json:"name"`
	Arch              string      `json:"arch"`
	OsType            string      `json:"osType"`
	RunPath           string      `json:"runPath"`
	Timeout           string      `json:"timeout"`
	Publisher         string      `json:"publisher"`
	Version           string      `json:"version"`
	PluginType_        interface{} `json:"pluginType"`
	HeartbeatInterval int         `json:"heartbeatInterval"`
	pluginTypeStr     string
}

func (pc *pluginConfig) PluginType() string {
	if pc.pluginTypeStr == "" {
		switch pc.PluginType_.(type) {
		case string:
			pt, _ := pc.PluginType_.(string)
			if pt == PLUGIN_ONCE {
				pc.pluginTypeStr = PLUGIN_ONCE
			} else if pt == PLUGIN_PERSIST {
				pc.pluginTypeStr = PLUGIN_PERSIST
			} else {
				pc.pluginTypeStr = PLUGIN_UNKNOWN
			}
		case float64:
			pt, _ := pc.PluginType_.(float64)
			if pt == float64(PLUGIN_ONCE_INT) {
				pc.pluginTypeStr = PLUGIN_ONCE
			} else if pt == float64(PLUGIN_PERSIST_INT) {
				pc.pluginTypeStr = PLUGIN_PERSIST
			} else {
				pc.pluginTypeStr = PLUGIN_UNKNOWN
			}
		case nil:
			pc.pluginTypeStr = PLUGIN_ONCE
		default:
			pc.pluginTypeStr = PLUGIN_UNKNOWN
		}
	}
	return pc.pluginTypeStr
}

type PluginManager struct {
	Verbose bool
	Yes     bool
}

var INSTALLEDPLUGINS string
var PLUGINDIR string

const Separator = string(filepath.Separator)

func NewPluginManager(verbose bool) (*PluginManager, error) {
	var err error
	PLUGINDIR, err = util.GetPluginPath()
	INSTALLEDPLUGINS = PLUGINDIR + Separator + "installed_plugins"
	if err != nil {
		return nil, err
	}
	return &PluginManager{
		Verbose: verbose,
		Yes:     true,
	}, nil
}

func loadInstalledPlugins() ([]PluginInfo, error) {
	installedPlugins := InstalledPlugins{}
	if util.CheckFileIsExist(INSTALLEDPLUGINS) {
		if _, err := unmarshalFile(INSTALLEDPLUGINS, &installedPlugins); err != nil {
			return nil, err
		}
		return installedPlugins.PluginList, nil
	}
	return installedPlugins.PluginList, nil
}

func dumpInstalledPlugins(pluginInfoList []PluginInfo) error {
	installedPlugins := InstalledPlugins{
		PluginList: pluginInfoList,
	}
	pluginInfoListStr, err := marshal(&installedPlugins)
	if err != nil {
		return err
	}
	err = util.WriteStringToFile(INSTALLEDPLUGINS, pluginInfoListStr)
	return err
}

func printPluginInfo(pluginInfoList *[]PluginInfo) {
	tbl := table.New("Name", "Version", "Publisher", "OsType", "Arch", "PluginType")
	for i := 0; i < len(*pluginInfoList); i++ {
		pluginInfo := (*pluginInfoList)[i]
		// 已删除的插件不打印
		if pluginInfo.IsRemoved {
			continue
		}
		tbl.AddRow(pluginInfo.Name, pluginInfo.Version, pluginInfo.Publisher, pluginInfo.OSType, pluginInfo.Arch, pluginInfo.PluginType())
	}
	tbl.Print()
	fmt.Println()
}

// get pluginInfo by name from online
func getPackageInfo(pluginName, version string, withArch bool) ([]PluginInfo, error) {
	arch := ""
	if withArch {
		arch, _ = getArch()
	}
	postValue := PluginListRequest{
		OsType:     "linux",
		PluginName: pluginName,
		Version:    version,
		Arch:       arch,
	}
	listRet := PluginListResponse{}
	if osutil.GetOsType() == osutil.OSWin {
		postValue.OsType = "windows"
	}
	postValueStr, err := marshal(&postValue)
	if err != nil {
		return listRet.PluginList, err
	}
	// http 请求尝试3次
	log.GetLogger().Infof("Request /plugin/list, params[%s]", string(postValueStr))
	ret, err := util.HttpPost(util.GetPluginListService(), postValueStr, "json")
	if err != nil {
		retry := 2
		for retry > 0 && err != nil {
			retry--
			// pluginlist接口有流控，等一下再重试
			time.Sleep(time.Duration(3) * time.Second)
			ret, err = util.HttpPost(util.GetPluginListService(), postValueStr, "json")
		}
	}
	if err != nil {
		return listRet.PluginList, err
	}
	if err := unmarshal(ret, &listRet); err != nil {
		return nil, err
	}
	return listRet.PluginList, nil
}

func getLocalPluginInfo(packageName, pluginVersion string) (*PluginInfo, error) {
	installedPlugins, err := loadInstalledPlugins()
	if err != nil {
		return nil, err
	}
	for _, plugin := range installedPlugins {
		// 存在该插件记录且未被删除
		if plugin.Name == packageName && !plugin.IsRemoved {
			if pluginVersion == "" || pluginVersion == plugin.Version {
				return &plugin, nil
			}
		}
	}
	return nil, nil
}

func getOnlinePluginInfo(packageName, version string) (archMatch *PluginInfo, archNotMatch []string, err error) {
	// request all arch pluginInfos
	var pluginList []PluginInfo
	pluginList, err = getPackageInfo(packageName, version, false)
	if err != nil {
		return nil, nil, err
	}
	localArch, _ := getArch()
	for idx, plugin := range pluginList {
		if plugin.Name == packageName {
			plugin.Arch = strings.ToLower(plugin.Arch)
			if plugin.Arch == "" || plugin.Arch == "all" || localArch == plugin.Arch {
				archMatch = &pluginList[idx]
			} else {
				archNotMatch = append(archNotMatch, plugin.Arch)
			}
		}
	}
	return
}

func (pm *PluginManager) List(pluginName string, local bool) (exitCode int, err error) {
	var pluginInfoList []PluginInfo
	exitCode = SUCCESS
	if local {
		pluginInfoList, err = loadInstalledPlugins()
		if err != nil {
			exitCode = LOAD_INSTALLEDPLUGINS_ERR
			fmt.Println("List " + LOAD_INSTALLEDPLUGINS_ERR_STR + "Load installed_plugins err: " + err.Error())
			return
		}
		if pluginName != "" {
			pluginList := []PluginInfo{}
			for _, pluginInfo := range pluginInfoList {
				if pluginInfo.Name == pluginName {
					pluginList = append(pluginList, pluginInfo)
				}
			}
			pluginInfoList = pluginList
		}
	} else {
		// just request pluginInfos with right arch
		pluginInfoList, err = getPackageInfo(pluginName, "", true)
		if err != nil {
			exitCode = GET_ONLINE_PACKAGE_INFO_ERR
			fmt.Println("List " + GET_ONLINE_PACKAGE_INFO_ERR_STR + "Get plugin info from online err: " + err.Error())
			return
		}
	}
	printPluginInfo(&pluginInfoList)
	return
}

// 打印常驻插件的状态，包括已删除的常驻插件
func (pm *PluginManager) ShowPluginStatus() (exitCode int, err error) {
	log.GetLogger().Infoln("Enter showPluginStatus")
	exitCode = SUCCESS
	installedPlugins, err := loadInstalledPlugins()
	if err != nil {
		exitCode = LOAD_INSTALLEDPLUGINS_ERR
		fmt.Println("ShowPluginStatus " + LOAD_INSTALLEDPLUGINS_ERR_STR + "Load installed_plugins err: " + err.Error())
		return
	}
	log.GetLogger().Infof("Count of installed plugins: %d", len(installedPlugins))
	statusList := []PluginStatus{}
	pluginPath := PLUGINDIR + Separator
	paramList := []string{"--status"}
	for _, plugin := range installedPlugins {
		timeout := 60
		code := 0
		if t, err := strconv.ParseInt(plugin.Timeout, 10, 0); err == nil {
			timeout = int(t)
		}
		if plugin.PluginType() == PLUGIN_PERSIST {
			status := PluginStatus{
				Name:    plugin.Name,
				Version: plugin.Version,
				Status:  PERSIST_FAIL,
			}
			if plugin.IsRemoved {
				status.Status = REMOVED
			} else {
				pluginDir := filepath.Join(pluginPath, plugin.Name, plugin.Version)
				env := []string{
					"PLUGIN_DIR=" + pluginDir,
				}
				cmdPath := filepath.Join(pluginDir, plugin.RunPath)
				code, err = pm.executePlugin(cmdPath, paramList, timeout, env, true)
				if code == 0 && err == nil {
					status.Status = PERSIST_RUNNING
				}
				if err != nil {
					log.GetLogger().Errorf("ShowPluginStatus: executePlugin err, pluginName[%s] pluginVersion[%s]", plugin.Name, plugin.Version)
				}
			}
			statusList = append(statusList, status)
		}
	}
	content, err := marshal(&statusList)
	if err != nil {
		log.GetLogger().Error("ShowPluginStatus err when marshal statusList, err: ", err.Error())
	}
	fmt.Println(content)
	return
}

func (pm *PluginManager) ExecutePlugin(file, pluginName, pluginId, params, separator, paramsV2, version string, local bool) (exitCode int, err error) {
	log.GetLogger().Infoln("Enter ExecutePlugin")
	if pm.Verbose {
		log.GetLogger().Infof("ExecutePlugin: file[%s], pluginName[%s], pluginId[%s], params[%s], separator[%s], paramsV2[%s], version[%s], local[%v]", file, pluginName, pluginId, params, separator, paramsV2, version, local)
	}
	var paramList []string
	timeout := 60
	if paramsV2 != "" {
		paramList, _ = shlex.Split(paramsV2)
	} else {
		if separator == "" {
			separator = ","
		}
		paramsSpace := strings.Replace(params, separator, " ", -1)
		paramList, _ = shlex.Split(paramsSpace)
	}
	if len(paramList) == 0 {
		paramList = nil
	}
	if file != "" {
		return pm.executePluginFromFile(file, paramList, timeout)
	}
	// execute plugin exe-file
	return pm.executePluginOnlineOrLocal(pluginName, pluginId, version, paramList, timeout, local)
}

// 根据插件名称删除插件，会删除该插件的整个目录（包括其中各版本的目录）
// 一次性插件：直接删除相应的目录并将installed_plugins中对应的插件标记为已删除（isRemoved=true）
// 常驻型插件：删除之前先调用插件的 --stop和 --uninstall，如果--uninstall退出码非0则不删除，否则像一次性插件一样删除目录并标记
func (pm *PluginManager) RemovePlugin(pluginName string) (exitCode int, err error) {
	defer func() {
		if exitCode != 0 || err != nil {
			fmt.Printf("RemovePlugin error, plugin[%s], err: %v\n", pluginName, err)
		} else {
			fmt.Printf("RemovePlugin success, plugin[%s]\n", pluginName)
		}
	}()
	var installedPlugins []PluginInfo
	installedPlugins, err = loadInstalledPlugins()
	if err != nil {
		exitCode = LOAD_INSTALLEDPLUGINS_ERR
		fmt.Println("RemovePlugin " + LOAD_INSTALLEDPLUGINS_ERR_STR + "Load installed_plugins err: " + err.Error())
		return
	}
	idx := -1
	for i := 0; i < len(installedPlugins); i++ {
		if installedPlugins[i].IsRemoved {
			continue
		}
		if installedPlugins[i].Name == pluginName {
			idx = i
			break
		}
	}
	if idx == -1 {
		exitCode = PACKAGE_NOT_FOUND
		fmt.Println("RemovePlugin " + PACKAGE_NOT_FOUND_STR + "plugin not exist " + pluginName)
		err = errors.New("Plugin " + pluginName + " not found in installed_plugins")
		return
	}
	pluginInfo := installedPlugins[idx]
	if pluginInfo.PluginType() == PLUGIN_PERSIST {
		// 常驻型插件
		var (
			envPluginDir    string
			envPrePluginDir string
		)
		cmdPath := filepath.Join(PLUGINDIR, pluginInfo.Name, pluginInfo.Version, pluginInfo.RunPath)
		envPluginDir = filepath.Join(PLUGINDIR, pluginInfo.Name, pluginInfo.Version)

		var timeout int
		if timeout, err = strconv.Atoi(pluginInfo.Timeout); err != nil {
			timeout = 60
		}
		env := []string{
			"PLUGIN_DIR=" + envPluginDir,
			"PRE_PLUGIN_DIR=" + envPrePluginDir,
		}
		// --stop 停止插件进程
		paramList := []string{"--stop"}
		pm.executePlugin(cmdPath, paramList, timeout, env, false)
		// --uninstall 卸载插件服务
		paramList = []string{"--uninstall"}
		exitCode, err = pm.executePlugin(cmdPath, paramList, timeout, env, false)
		if exitCode != 0 || err != nil {
			return
		}
		// 更新installed_plugins文件
		installedPlugins[idx].IsRemoved = true // 标记为已删除
		if err = dumpInstalledPlugins(installedPlugins); err != nil {
			exitCode = DUMP_INSTALLEDPLUGINS_ERR
			fmt.Println("RemovePlugin " + DUMP_INSTALLEDPLUGINS_ERR_STR + "Update installed_plugins file err: " + err.Error())
			return
		}
		if err = pm.ReportPluginStatus(installedPlugins[idx].Name, installedPlugins[idx].Version, REMOVED); err != nil {
			log.GetLogger().Errorf("Plugin[%s] is removed, but report the removed plugin to server error: %s", installedPlugins[idx].Name, err.Error())
		}
		// 删除插件目录
		pluginDir := filepath.Join(PLUGINDIR, pluginInfo.Name)
		if err = os.RemoveAll(pluginDir); err != nil {
			exitCode = REMOVE_FILE_ERR
			tip := fmt.Sprintf("Plugin[%s] is removed, but remove plugin directory err, pluginDir[%s], err: %s", installedPlugins[idx].Name, pluginDir, err.Error())
			fmt.Println("RemovePlugin " + REMOVE_FILE_ERR_STR + tip)
			return
		}
	} else {
		// 一次性插件
		installedPlugins[idx].IsRemoved = true // 标记为已删除
		// 更新installed_plugins文件
		if err = dumpInstalledPlugins(installedPlugins); err != nil {
			exitCode = DUMP_INSTALLEDPLUGINS_ERR
			fmt.Println("RemovePlugin " + DUMP_INSTALLEDPLUGINS_ERR_STR + "Update installed_plugins file err: " + err.Error())
			return
		}
		if err = pm.ReportPluginStatus(installedPlugins[idx].Name, installedPlugins[idx].Version, REMOVED); err != nil {
			log.GetLogger().Errorf("Plugin[%s] is removed, but report the removed plugin to server error: %s", installedPlugins[idx].Name, err.Error())
		}
		// 删除插件目录
		pluginDir := filepath.Join(PLUGINDIR, pluginInfo.Name)
		if err = os.RemoveAll(pluginDir); err != nil {
			exitCode = REMOVE_FILE_ERR
			tip := fmt.Sprintf("Remove plugin directory err, pluginDir[%s], err: %s", pluginDir, err.Error())
			fmt.Println("RemovePlugin " + REMOVE_FILE_ERR_STR + tip)
			return
		}
	} 
	return
}

// run plugin from plugin_file.zip
func (pm *PluginManager) executePluginFromFile(file string, paramList []string, timeout int) (exitCode int, err error) {
	log.GetLogger().Infof("Enter executePluginFromFile")
	if pm.Verbose {
		fmt.Println("Execute plugin from file: ", file)
	}
	localArch, _ := getArch()
	exitCode = SUCCESS
	var (
		// 执行插件时要注入的环境变量
		envPluginDir    string // 当前执行的插件的执行目录
		envPrePluginDir string // 如果已有同名的其他版本插件，表示原有同名插件的执行目录；否则为空
	)
	if !util.CheckFileIsExist(file) {
		err = errors.New("File not exist: " + file)
		exitCode = PACKAGE_NOT_FOUND
		fmt.Println("ExecutePluginFromFile " + PACKAGE_NOT_FOUND_STR + "Package file not exist: " + file)
		return
	}
	cmdPath := ""
	idx := strings.LastIndex(file, Separator)
	pluginName := file
	dirName := "."
	if idx > 0 {
		pluginName = file[idx+1:]
		dirName = file[:idx]
	}
	idx = strings.Index(pluginName, ".zip")
	if idx <= 0 {
		err = errors.New("Package file not a zip file: " + file)
		exitCode = PACKAGE_FORMART_ERR
		fmt.Println("ExecutePluginFromFile " + PACKAGE_FORMART_ERR_STR + "Package file isn`t a zip file: " + file)
		return
	}
	pluginName = pluginName[:idx]
	dirName = filepath.Join(dirName, pluginName)
	util.MakeSurePath(dirName)
	if pm.Verbose {
		fmt.Printf("Unzip to %s ...\n", dirName)
	}
	unzipdir := dirName
	if err = util.Unzip(file, unzipdir); err != nil {
		exitCode = UNZIP_ERR
		tip := fmt.Sprintf("Unzip err, file is [%s], target dir is [%s], err is [%s]", file, unzipdir, err.Error())
		fmt.Println("ExecutePluginFromFile " + UNZIP_ERR_STR + tip)
		return
	}
	config_path := filepath.Join(dirName, "config.json")
	if !util.CheckFileIsExist(config_path) {
		config_path = filepath.Join(dirName, pluginName, "config.json")
		if !util.CheckFileIsExist(config_path) {
			exitCode = PLUGIN_FORMAT_ERR
			fmt.Println("ExecutePluginFromFile " + PLUGIN_FORMAT_ERR_STR + "File config.json not exist.")
			return
		}
		dirName = filepath.Join(dirName, pluginName)
	}
	config := pluginConfig{}
	var content []byte
	if content, err = unmarshalFile(config_path, &config); err != nil {
		exitCode = UNMARSHAL_ERR
		tip := fmt.Sprintf("Unmarshal config.json err, config.json is [%s], err is [%s]", string(content), err.Error())
		fmt.Println("ExecutePluginFromFile " + UNMARSHAL_ERR_STR + tip)
		return
	}
	// 检查系统类型和架构是否符合
	if config.OsType != "" && strings.ToLower(config.OsType) != osutil.GetOsType() {
		err = errors.New("Plugin ostype not suit for this system")
		exitCode = PLUGIN_FORMAT_ERR
		tip := fmt.Sprintf("Plugin ostype[%s] not suit for this system[%s]", config.OsType, osutil.GetOsType())
		fmt.Println("ExecutePluginFromFile " + PLUGIN_FORMAT_ERR_STR + tip)
		return
	}
	if config.Arch != "" && strings.ToLower(config.Arch) != "all" && strings.ToLower(config.Arch) != localArch {
		err = errors.New("Plugin arch not suit for this system")
		exitCode = PLUGIN_FORMAT_ERR
		tip := fmt.Sprintf("Plugin arch[%s] not suit for this system[%s]", config.Arch, localArch)
		fmt.Println("ExecutePluginFromFile " + PLUGIN_FORMAT_ERR_STR + tip)
		return
	}
	var installedPlugins []PluginInfo
	installedPlugins, err = loadInstalledPlugins()
	if err != nil {
		exitCode = LOAD_INSTALLEDPLUGINS_ERR
		fmt.Println("ExecutePluginFromFile " + LOAD_INSTALLEDPLUGINS_ERR_STR + "Load installed_plugins err: " + err.Error())
		return
	}
	var plugin *PluginInfo
	pluginIndex := -1
	for idx, plugininfo := range installedPlugins {
		if plugininfo.Name == config.Name {
			plugin = &plugininfo
			pluginIndex = idx
			break
		}
	}
	if plugin != nil && plugin.IsRemoved {
		// 之前的同名插件已经被删除，相当于重新安装
		installedPlugins = DeletePluginInfoByIdx(installedPlugins, pluginIndex)
		pluginIndex = -1
		plugin = nil
	}
	if plugin != nil {
		envPrePluginDir = filepath.Join(PLUGINDIR, plugin.Name, plugin.Version)
		// has installed, check version
		if versionutil.CompareVersion(config.Version, plugin.Version) <= 0 {
			if !pm.Yes {
				yn := ""
				for {
					fmt.Printf("[%s %s] has installed, this package version[%s] is not newer, still install ? [y/n]: \n", plugin.Name, plugin.Version, config.Version)
					fmt.Scanln(&yn)
					if yn == "y" || yn == "n" {
						break
					}
				}
				if yn == "n" {
					log.GetLogger().Infoln("Execute plugin cancel...")
					fmt.Println("Execute plugin cancel...")
					return
				}
			}
			fmt.Printf("[%s %s] has installed, this package version[%s] is not newer, still install...\n", plugin.Name, plugin.Version, config.Version)
		} else {
			fmt.Printf("[%s %s] has installed, this package version[%s] is newer, keep install...\n", plugin.Name, plugin.Version, config.Version)
		}
	}
	if pluginIndex == -1 {
		plugin = &PluginInfo{
			Timeout: "60",
		}
	}
	if t, err := strconv.Atoi(config.Timeout); err != nil {
		config.Timeout = plugin.Timeout
	} else {
		timeout = t
	}
	plugin.Name = config.Name
	plugin.Arch = config.Arch
	plugin.OSType = config.OsType
	plugin.RunPath = config.RunPath
	plugin.Timeout = config.Timeout
	plugin.Publisher = config.Publisher
	plugin.Version = config.Version
	plugin.SetPluginType(config.PluginType())
	plugin.Url = "local"
	if config.HeartbeatInterval <= 0 {
		plugin.HeartbeatInterval = 60
	} else {
		plugin.HeartbeatInterval = config.HeartbeatInterval
	}
	var md5Str string
	md5Str, err = util.ComputeMd5(file)
	if err != nil {
		exitCode = MD5_CHECK_FAIL
		fmt.Println("ExecutePluginFromFile " + MD5_CHECK_FAIL_STR + "Compute md5 of plugin file err: " + err.Error())
		return
	}
	plugin.Md5 = md5Str

	pluginPath := filepath.Join(PLUGINDIR, plugin.Name, plugin.Version)
	envPluginDir = pluginPath
	util.MakeSurePath(pluginPath)
	util.CopyDir(dirName, pluginPath)
	cmdPath = filepath.Join(pluginPath, config.RunPath)
	if !util.CheckFileIsExist(cmdPath) {
		log.GetLogger().Infoln("Cmd file not exist: ", cmdPath)
		err = errors.New("Cmd file not exist: " + cmdPath)
		exitCode = PLUGIN_FORMAT_ERR
		fmt.Println("ExecutePluginFromFile " + PLUGIN_FORMAT_ERR_STR + "Executable file not exist.")
		return
	}
	if osutil.GetOsType() != osutil.OSWin {
		if err = exec.Command("chmod", "744", cmdPath).Run(); err != nil {
			exitCode = EXECUTABLE_PERMISSION_ERR
			fmt.Println("ExecutePluginFromFile " + EXECUTABLE_PERMISSION_ERR_STR + "Make plugin file executable err: " + err.Error())
			return
		}
	}
	if pluginIndex == -1 {
		plugin.PluginID = "local_" + plugin.Name + "_" + plugin.Version
		installedPlugins = append(installedPlugins, *plugin)
	} else {
		installedPlugins[pluginIndex] = *plugin
	}
	if err = dumpInstalledPlugins(installedPlugins); err != nil {
		exitCode = DUMP_INSTALLEDPLUGINS_ERR
		fmt.Println("ExecutePluginFromFile " + DUMP_INSTALLEDPLUGINS_ERR_STR + "Update installed_plugins file err: " + err.Error())
		return
	}
	fmt.Printf("Plugin[%s] installed!\n", plugin.Name)
	os.RemoveAll(unzipdir)

	env := []string{
		"PLUGIN_DIR=" + envPluginDir,
		"PRE_PLUGIN_DIR=" + envPrePluginDir,
	}
	exitCode, err = pm.executePlugin(cmdPath, paramList, timeout, env, false)
	// 如果是常驻插件，且调用的接口有可能改变插件状态，需要主动上报一次插件状态
	if plugin.PluginType() == PLUGIN_PERSIST && needReportStatus(paramList) {
		status, err := pm.CheckAndReportPlugin(plugin.Name, plugin.Version, cmdPath, timeout, env)
		log.GetLogger().Infof("CheckAndReportPlugin : pluginName[%s] pluginVersion[%s] cmdPath[%s] timeout[%d] env[%v] status[%s], err: %v", plugin.Name, plugin.Version, cmdPath, timeout, env, status, err)
	}
	return
}

func (pm *PluginManager) executePluginOnlineOrLocal(pluginName string, pluginId string, pluginVersion string, paramList []string, timeout int, local bool) (exitCode int, err error) {
	log.GetLogger().Info("Enter executePluginOnlineOrLocal")
	useLocal := false
	cmdPath := ""
	pluginType := PLUGIN_ONCE
	var (
		// 执行插件时要注入的环境变量
		envPluginDir    string // 当前执行的插件的执行目录
		envPrePluginDir string // 如果已有同名插件，表示已有同名插件的执行目录；否则为空
	)
	localArch, _ := getArch()
	if !local {
		// didn't set --local, so local & online both try
		var localInfo *PluginInfo = nil
		var onlineInfo *PluginInfo = nil
		var onlineOtherArch []string
		localInfo, err = getLocalPluginInfo(pluginName, pluginVersion)
		if err != nil {
			exitCode = LOAD_INSTALLEDPLUGINS_ERR
			fmt.Println("ExecutePluginOnlineOrLocal " + LOAD_INSTALLEDPLUGINS_ERR_STR + "Load installed_plugins err: " + err.Error())
			return
		}
		onlineInfo, onlineOtherArch, err = getOnlinePluginInfo(pluginName, pluginVersion)
		if err != nil {
			exitCode = GET_ONLINE_PACKAGE_INFO_ERR
			fmt.Println("ExecutePluginOnlineOrLocal " + GET_ONLINE_PACKAGE_INFO_ERR_STR + "Get plugin info from online err: " + err.Error())
			return
		}
		if localInfo != nil {
			if onlineInfo != nil {
				// 本地和线上版本一致，使用本地插件文件
				if versionutil.CompareVersion(localInfo.Version, onlineInfo.Version) == 0 {
					log.GetLogger().Infof("ExecutePluginOnlineOrLocal: Plugin[%s], local version[%s] same to online version[%s], so use local package")
					useLocal = true
				} else {
					// 本地和线上版本不一致，使用线上版本
					log.GetLogger().Infof("ExecutePluginOnlineOrLocal: Plugin[%s], local version[%s] different from online version[%s], so use online package")
				}
			} else {
				useLocal = true
			}
		} else {
			if onlineInfo == nil {
				var tip string
				if len(onlineOtherArch) == 0 {
					tip = fmt.Sprintf("Could not found both local and online, package[%s] version[%s]\n", pluginName, pluginVersion)
				} else {
					localArch, _ = getArch()
					tip = fmt.Sprintf("Could not found local package[%s] version[%s], found online package but it`s arch[%s] not match local_arch[%s] \n", pluginName, pluginVersion, strings.Join(onlineOtherArch, ", "), localArch)
				}
				exitCode = PACKAGE_NOT_FOUND
				fmt.Print("ExecutePluginOnlineOrLocal " + PACKAGE_NOT_FOUND_STR + tip)
				return
			}
		}
		// use local package
		if useLocal {
			if t, err := strconv.Atoi(localInfo.Timeout); err == nil {
				timeout = t
			}
			pluginPath := filepath.Join(PLUGINDIR, localInfo.Name, localInfo.Version)
			envPluginDir = pluginPath
			pluginName = localInfo.Name
			pluginVersion = localInfo.Version
			pluginType = localInfo.PluginType()
			cmdPath = filepath.Join(pluginPath, localInfo.RunPath)
		} else {
			// pull package
			filePath := filepath.Join(PLUGINDIR, pluginName+".zip")
			log.GetLogger().Infof("Downloading package from [%s], save to [%s] ", onlineInfo.Url, filePath)
			if err = util.HttpDownlod(onlineInfo.Url, filePath); err != nil {
				retry := 2
				for retry > 0 && err != nil {
					retry--
					time.Sleep(time.Second * 3)
					err = util.HttpDownlod(onlineInfo.Url, filePath)
				}
				if err != nil {
					exitCode = DOWNLOAD_FAIL
					tip := fmt.Sprintf("Downloading package failed, plugin.Url is [%s], err is [%s]", onlineInfo.Url, err.Error())
					fmt.Println("ExecutePluginOnlineOrLocal " + DOWNLOAD_FAIL_STR + tip)
					return
				}
			}
			log.GetLogger().Infoln("Check MD5...")
			md5Str := ""
			md5Str, err = util.ComputeMd5(filePath)
			if err != nil {
				exitCode = MD5_CHECK_FAIL
				tip := fmt.Sprintf("Compute md5 of plugin file[%s] err, plugin.Url is [%s], err is [%s]", filePath, onlineInfo.Url, err.Error())
				fmt.Println("ExecutePluginOnlineOrLocal " + MD5_CHECK_FAIL_STR + tip)
				return
			}
			if strings.ToLower(md5Str) != strings.ToLower(onlineInfo.Md5) {
				log.GetLogger().Errorf("Md5 not match, onlineInfo.Md5[%s], package file md5[%s]\n", onlineInfo.Md5, md5Str)
				err = errors.New("Md5 not macth")
				exitCode = MD5_CHECK_FAIL
				tip := fmt.Sprintf("Md5 not match, onlineInfo.Md5 is [%s], real md5 is [%s], plugin.Url is [%s]", onlineInfo.Md5, md5Str, onlineInfo.Url)
				fmt.Println("ExecutePluginOnlineOrLocal " + MD5_CHECK_FAIL_STR + tip)
				return
			}
			unzipdir := filepath.Join(PLUGINDIR, onlineInfo.Name, onlineInfo.Version)
			util.MakeSurePath(unzipdir)
			log.GetLogger().Infoln("Unzip package...")
			if err = util.Unzip(filePath, unzipdir); err != nil {
				exitCode = UNZIP_ERR
				tip := fmt.Sprintf("Unzip package err, plugin.Url is [%s], err is [%s]", onlineInfo.Url, err.Error())
				fmt.Println("ExecutePluginOnlineOrLocal " + UNZIP_ERR_STR + tip)
				return
			}
			os.RemoveAll(filePath)
			config_path := filepath.Join(unzipdir, "config.json")
			if !util.CheckFileIsExist(config_path) {
				exitCode = PLUGIN_FORMAT_ERR
				fmt.Println("ExecutePluginOnlineOrLocal " + PLUGIN_FORMAT_ERR_STR + "File config.json not exist.")
				return
			}
			config := pluginConfig{}
			var content []byte
			if content, err = unmarshalFile(config_path, &config); err != nil {
				exitCode = UNMARSHAL_ERR
				tip := fmt.Sprintf("Unmarshal config.json err, config.json is [%s], err is [%s]", string(content), err.Error())
				fmt.Println("ExecutePluginOnlineOrLocal " + UNMARSHAL_ERR_STR + tip)
				return
			}
			if config.HeartbeatInterval <= 0 {
				config.HeartbeatInterval = 60
			}
			if config.PluginType() != onlineInfo.PluginType() {
				tip := fmt.Sprintf("config.PluginType[%s] not match to pluginType[%s]", config.PluginType(), onlineInfo.PluginType())
				err = errors.New(tip)
				exitCode = PLUGIN_FORMAT_ERR
				fmt.Println("ExecutePluginOnlineOrLocal " + PLUGIN_FORMAT_ERR_STR + tip)
				return
			}
			// 接口返回的插件信息中没有HeartbeatInterval字段，需要以插件包中的config.json为准
			onlineInfo.HeartbeatInterval = config.HeartbeatInterval
			onlineInfo.SetPluginType(config.PluginType())
			envPluginDir = filepath.Join(PLUGINDIR, config.Name, config.Version)
			pluginName = config.Name
			pluginVersion = config.Version
			pluginType = config.PluginType()
			if t, err := strconv.Atoi(config.Timeout); err == nil {
				timeout = t
			}
			cmdPath = filepath.Join(unzipdir, config.RunPath)
			// 检查系统类型和架构是否符合
			if strings.ToLower(onlineInfo.OSType) != osutil.GetOsType() {
				err = errors.New("Plugin ostype not suit for this system")
				exitCode = PLUGIN_FORMAT_ERR
				tip := fmt.Sprintf("Plugin ostype[%s] not suit for this system[%s], plugin.Url is [%s]", onlineInfo.OSType, osutil.GetOsType(), onlineInfo.Url)
				fmt.Println("ExecutePluginOnlineOrLocal " + PLUGIN_FORMAT_ERR_STR + tip)
				return
			}
			if strings.ToLower(onlineInfo.Arch) != "all" && strings.ToLower(onlineInfo.Arch) != localArch {
				err = errors.New("Plugin arch not suit for this system")
				exitCode = PLUGIN_FORMAT_ERR
				tip := fmt.Sprintf("Plugin arch[%s] not suit for this system[%s], plugin.Url is [%s]", onlineInfo.Arch, localArch, onlineInfo.Url)
				fmt.Println("ExecutePluginOnlineOrLocal " + PLUGIN_FORMAT_ERR_STR + tip)
				return
			}
			if !util.CheckFileIsExist(cmdPath) {
				log.GetLogger().Infoln("Cmd file not exist: ", cmdPath)
				err = errors.New("Cmd file not exist: " + cmdPath)
				exitCode = PLUGIN_FORMAT_ERR
				fmt.Println("ExecutePluginOnlineOrLocal " + PLUGIN_FORMAT_ERR_STR + "Executable file not exist.")
				return
			}
			if osutil.GetOsType() != osutil.OSWin {
				if err = exec.Command("chmod", "744", cmdPath).Run(); err != nil {
					exitCode = EXECUTABLE_PERMISSION_ERR
					tip := fmt.Sprintf("Make plugin file executable err, plugin.Url is [%s], err is [%s]", onlineInfo.Url, err.Error())
					fmt.Println("ExecutePluginOnlineOrLocal " + EXECUTABLE_PERMISSION_ERR_STR + tip)
					return
				}
			}
			// update INSTALLEDPLUGINS file
			var installedPlugins []PluginInfo
			installedPlugins, err = loadInstalledPlugins()
			if err != nil {
				exitCode = LOAD_INSTALLEDPLUGINS_ERR
				fmt.Println("ExecutePluginOnlineOrLocal " + LOAD_INSTALLEDPLUGINS_ERR_STR + "Load installed_plugins err: " + err.Error())
				return
			}
			pluginIndex := -1
			for idx, plugininfo := range installedPlugins {
				if plugininfo.Name == onlineInfo.Name {
					pluginIndex = idx
					break
				}
			}
			if pluginIndex != -1 && installedPlugins[pluginIndex].IsRemoved {
				installedPlugins = DeletePluginInfoByIdx(installedPlugins, pluginIndex) // 删除掉被标记为”已删除“的插件记录
				pluginIndex = -1
			}
			if pluginIndex == -1 {
				installedPlugins = append(installedPlugins, *onlineInfo)
			} else {
				plugininfo := installedPlugins[pluginIndex]
				envPrePluginDir = filepath.Join(PLUGINDIR, plugininfo.Name, plugininfo.Version)
				installedPlugins[pluginIndex] = *onlineInfo
			}
			err = dumpInstalledPlugins(installedPlugins)
			if err != nil {
				exitCode = DUMP_INSTALLEDPLUGINS_ERR
				fmt.Println("ExecutePluginOnlineOrLocal " + DUMP_INSTALLEDPLUGINS_ERR_STR + "Update installed_plugins file err: " + err.Error())
				return
			}
		}
	} else {
		// execute local plugin
		var localInfo *PluginInfo
		localInfo, err = getLocalPluginInfo(pluginName, pluginVersion)
		if err != nil {
			exitCode = LOAD_INSTALLEDPLUGINS_ERR
			fmt.Println("ExecutePluginOnlineOrLocal " + LOAD_INSTALLEDPLUGINS_ERR_STR + "Load installed_plugins err: " + err.Error())
			return
		} else if localInfo == nil {
			tip := fmt.Sprintf("Could not found local package [%s]", pluginName)
			err = errors.New("Could not found package")
			exitCode = PACKAGE_NOT_FOUND
			fmt.Println("ExecutePluginOnlineOrLocal " + PACKAGE_NOT_FOUND_STR + tip)
			return
		}
		envPluginDir = filepath.Join(PLUGINDIR, localInfo.Name, localInfo.Version)
		pluginName = localInfo.Name
		pluginVersion = localInfo.Version
		pluginType = localInfo.PluginType()
		if t, err := strconv.Atoi(localInfo.Timeout); err == nil {
			timeout = t
		}
		cmdPath = filepath.Join(envPluginDir, localInfo.RunPath)
	}

	env := []string{
		"PLUGIN_DIR=" + envPluginDir,
		"PRE_PLUGIN_DIR=" + envPrePluginDir,
	}
	exitCode, err = pm.executePlugin(cmdPath, paramList, timeout, env, false)
	// 如果是常驻插件，且调用的接口有可能改变插件状态，需要主动上报一次插件状态
	if pluginType == PLUGIN_PERSIST && needReportStatus(paramList) {
		status, err := pm.CheckAndReportPlugin(pluginName, pluginVersion, cmdPath, timeout, env)
		log.GetLogger().Infof("CheckAndReportPlugin : pluginName[%s] pluginVersion[%s] cmdPath[%s] timeout[%d] env[%s] status[%s], err: %v", pluginName, pluginVersion, cmdPath, timeout, strings.Join(env, ","), status, err)
	}
	return
}

func (pm *PluginManager) executePlugin(cmdPath string, paramList []string, timeout int, env []string, quiet bool) (exitCode int, err error) {
	log.GetLogger().Infof("Enter executePlugin, cmdPath[%s] paramList[%v] paramCount[%d] timeout[%d]\n", cmdPath, paramList, len(paramList), timeout)
	if !util.CheckFileIsExist(cmdPath) {
		log.GetLogger().Infoln("Cmd file not exist: ", cmdPath)
		err = errors.New("Cmd file not exist: " + cmdPath)
		exitCode = PLUGIN_FORMAT_ERR
		fmt.Println("ExecutePlugin " + PLUGIN_FORMAT_ERR_STR + "Executable file not exist.")
		return
	}
	if pm.Verbose {
		fmt.Printf("Run cmd: %s, params: %v\n", cmdPath, paramList)
	}

	processCmd := process.NewProcessCmd()
	// set environment variable
	if env != nil && len(env) > 0 {
		processCmd.SetEnv(env)
	}
	status := process.Success
	if quiet {
		exitCode, status, err = processCmd.SyncRun("", cmdPath, paramList, nil, nil, os.Stdin, nil, timeout)
	} else {
		exitCode, status, err = processCmd.SyncRun("", cmdPath, paramList, os.Stdout, os.Stderr, os.Stdin, nil, timeout)
	}
	if status == process.Fail {
		exitCode = EXECUTE_FAILED
	} else if status == process.Timeout {
		exitCode = EXECUTE_TIMEOUT
	}
	if !quiet {
		switch exitCode {
		case EXECUTE_FAILED:
			tip := fmt.Sprintf("Execute plugin failed, err: %v", err)
			fmt.Println("executePlugin " + EXECUTE_FAILED_STR + tip)
		case EXECUTE_TIMEOUT:
			tip := fmt.Sprintf("Execute plugin timeout, timeout[%d] err: %v", timeout, err)
			fmt.Println("executePlugin " + EXECUTE_TIMEOUT_STR + tip)
		}
	}
	log.GetLogger().Info(fmt.Sprintf("executePlugin: cmdPath: %s, params: %+q, exitCode: %d, timeout: %d, env: %v, err: %v\n", cmdPath, paramList, exitCode, timeout, env, err))
	return
}

func (pm *PluginManager) VerifyPlugin(url, params, separator, paramsV2 string) (exitCode int, err error) {
	log.GetLogger().Infof("Enter VerufyPlugin url[%s] params[%s] separator[%s]\n", url, params, separator)
	var paramList []string
	timeout := 60
	cmdPath := ""
	var (
		// 执行插件时要注入的环境变量
		envPluginDir    string // 当前执行的插件的执行目录
		envPrePluginDir string // 如果已有同名插件，表示已有同名插件的执行目录；否则为空
	)
	localArch, _ := getArch()
	if paramsV2 != "" {
		paramList, _ = shlex.Split(paramsV2)
	} else {
		if separator == "" {
			separator = ","
		}
		paramsSpace := strings.Replace(params, separator, " ", -1)
		paramList, _ = shlex.Split(paramsSpace)
	}
	if len(paramList) == 0 {
		paramList = nil
	}

	// pull package
	fileName := url[strings.LastIndex(url, "/")+1:]
	filePath := PLUGINDIR + Separator + fileName
	log.GetLogger().Infoln("Downloading package from ", url)
	if len(url) > 4 && url[:4] == "http" {
		if err = util.HttpDownlod(url, filePath); err != nil {
			exitCode = DOWNLOAD_FAIL
			tip := fmt.Sprintf("Downloading package failed, url is [%s], err is [%s]", url, err.Error())
			fmt.Println("VerifyPlugin " + DOWNLOAD_FAIL_STR + tip)
			return
		}
	} else {
		if err = FileProtocolDownload(url, filePath); err != nil {
			exitCode = DOWNLOAD_FAIL
			tip := fmt.Sprintf("Downloading package failed, url is [%s], err is [%s]", url, err.Error())
			fmt.Println("VerifyPlugin " + DOWNLOAD_FAIL_STR + tip)
			return
		}
	}

	unzipdir := filepath.Join(PLUGINDIR, "verify_plugin_test")
	util.MakeSurePath(unzipdir)
	log.GetLogger().Infoln("Unzip package...")
	if err = util.Unzip(filePath, unzipdir); err != nil {
		exitCode = UNZIP_ERR
		tip := fmt.Sprintf("Unzip package err, url is [%s], err is [%s]", url, err.Error())
		fmt.Println("VerifyPlugin " + UNZIP_ERR_STR + tip)
		return
	}
	os.RemoveAll(filePath)

	configPath := filepath.Join(unzipdir, "config.json")
	if !util.CheckFileIsExist(configPath) {
		err = errors.New("Can not find the config.json")
		exitCode = PLUGIN_FORMAT_ERR
		fmt.Println("VerifyPlugin " + PLUGIN_FORMAT_ERR_STR + "File config.json not exist.")
		return
	}
	config := pluginConfig{}
	var content []byte
	if content, err = unmarshalFile(configPath, &config); err != nil {
		exitCode = UNMARSHAL_ERR
		tip := fmt.Sprintf("Unmarshal config.json err, config.json is [%s], err is [%s]", string(content), err.Error())
		fmt.Println("VerifyPlugin " + UNMARSHAL_ERR_STR + tip)
		return
	}
	// 检查系统类型和架构是否符合
	if config.OsType != "" && strings.ToLower(config.OsType) != osutil.GetOsType() {
		err = errors.New("Plugin ostype not suit for this system")
		exitCode = PLUGIN_FORMAT_ERR
		tip := fmt.Sprintf("Plugin ostype[%s] not suit for this system[%s], url is [%s]", config.OsType, osutil.GetOsType(), url)
		fmt.Println("VerifyPlugin " + PLUGIN_FORMAT_ERR_STR + tip)
		return
	}
	if config.Arch != "" && strings.ToLower(config.Arch) != "all" && strings.ToLower(config.Arch) != localArch {
		err = errors.New("Plugin arch not suit for this system")
		exitCode = PLUGIN_FORMAT_ERR
		tip := fmt.Sprintf("Plugin arch[%s] not suit for this system[%s], url is [%s]", config.Arch, localArch, url)
		fmt.Println("VerifyPlugin " + PLUGIN_FORMAT_ERR_STR + tip)
		return
	}

	runPath := config.RunPath
	timeoutStr := config.Timeout
	envPluginDir = unzipdir
	cmdPath = filepath.Join(unzipdir, runPath)
	if !util.CheckFileIsExist(cmdPath) {
		err = errors.New("Can not find the cmd file")
		exitCode = PLUGIN_FORMAT_ERR
		fmt.Println("VerifyPlugin " + PLUGIN_FORMAT_ERR_STR + "Executable file not exist.")
		return
	}
	if osutil.GetOsType() != osutil.OSWin {
		err = exec.Command("chmod", "744", cmdPath).Run()
		if err != nil {
			exitCode = EXECUTABLE_PERMISSION_ERR
			fmt.Println("VerifyPlugin " + EXECUTABLE_PERMISSION_ERR_STR + "Make plugin file executable err: " + err.Error())
			return
		}
	}
	timeout = 60
	if t, err := strconv.Atoi(timeoutStr); err != nil {
		fmt.Println("config.Timeout is invalid: ", config.Timeout)
	} else {
		timeout = t
	}

	env := []string{
		"PLUGIN_DIR=" + envPluginDir,
		"PRE_PLUGIN_DIR=" + envPrePluginDir,
	}
	return pm.executePlugin(cmdPath, paramList, timeout, env, false)
}

// 向服务端上报某个插件状态
func (pm *PluginManager) ReportPluginStatus(pluginName, pluginVersion, status string) error {
	if len(pluginName) > PLUGIN_NAME_MAXLEN {
		pluginName = pluginName[:PLUGIN_NAME_MAXLEN]
	}
	if len(pluginVersion) > PLUGIN_VERSION_MAXLEN {
		pluginVersion = pluginVersion[:PLUGIN_VERSION_MAXLEN]
	}
	pluginStatusRequest := PluginStatusResquest{
		Plugin: []PluginStatus{
			{
				Name:    pluginName,
				Version: pluginVersion,
				Status:  status,
			},
		},
	}
	requestPayloadBytes, err := json.Marshal(pluginStatusRequest)
	if err != nil {
		log.GetLogger().WithError(err).Error("ReportPluginStatus: pluginStatusList marshal err: " + err.Error())
		return err
	}
	requestPayload := string(requestPayloadBytes)
	url := util.GetPluginHealthService()
	_, err = util.HttpPost(url, requestPayload, "")

	for i := 0; i < 3 && err != nil; i++ {
		log.GetLogger().Infof("ReportPluginStatus: upload pluginStatusList fail, need retry: %s", requestPayload)
		time.Sleep(time.Duration(2) * time.Second)
		_, err = util.HttpPost(url, requestPayload, "")
	}
	return err
}

// 检查并上报常驻插件状态
func (pm *PluginManager) CheckAndReportPlugin(pluginName, pluginVersion, cmdPath string, timeout int, env []string) (status string, err error) {
	exitCode := 0
	status = PERSIST_UNKNOWN
	exitCode, err = pm.executePlugin(cmdPath, []string{"--status"}, timeout, env, true)
	if err != nil {
		return
	}
	if exitCode != 0 {
		status = PERSIST_FAIL
	} else {
		status = PERSIST_RUNNING
	}
	return status, pm.ReportPluginStatus(pluginName, pluginVersion, status)
}

func needReportStatus(paramsList []string) bool {
	for _, p := range paramsList {
		for _, pp := range NEED_REFRESH_STATUS_API {
			if p == pp {
				return true
			}
		}
	}
	return false
}
