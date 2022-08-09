package pluginmanager

import (
	"github.com/aliyun/aliyun_assist_client/agent/util/process"
	"github.com/aliyun/aliyun_assist_client/agent/log"
	"io"
	"syscall"
)

func syncRunKillGroup(workingDir string, commandName string, commandArguments []string, stdoutWriter io.Writer, stderrWriter io.Writer,
	timeOut int) (exitCode int, status int, err error) {
	processCmd := process.NewProcessCmd()
	// SyncRun 中设置了进程组id和新起的进程id一致。SyncRun返回后调用系统调用kill掉进程组
	exitCode, status, err = processCmd.SyncRun(workingDir, commandName, commandArguments, stdoutWriter, stderrWriter, nil, nil, timeOut)
	if err != nil && err.Error() == "timeout" {
		log.GetLogger().Errorf("command timeout, will kill process group[%d]", processCmd.Pid())
		_ = syscall.Kill(-(processCmd.Pid()), syscall.SIGKILL)
	}
	return exitCode, status, err
}
