package channel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"bou.ke/monkey"
	"github.com/aliyun/aliyun_assist_client/agent/util"
	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/assert"
)

func TestGshellChannel(t *testing.T) {
	httpmock.Activate()
	util.NilRequest.Set()
	defer util.NilRequest.Clear()
	defer httpmock.DeactivateAndReset()
	const mockRegion = "cn-test100"
	util.MockMetaServer(mockRegion)

	httpmock.RegisterResponder("POST",
		fmt.Sprintf("https://%s.axt.aliyun.com/luban/api/metrics", mockRegion),
		func(h *http.Request) (*http.Response, error) {
			return httpmock.NewStringResponse(200, "success"), nil
		})
	httpmock.RegisterResponder("POST",
		fmt.Sprintf("https://%s.axt.aliyun.com/luban/api/gshell", mockRegion),
		func(h *http.Request) (*http.Response, error) {
			gshellstatus := gshellStatus{
				Code: 100,
				GshellSupport: "true",
				InstanceID: "gshell-id",
				RequestID: "request-id",
				Retry: 2,
			}
			resp, err := json.Marshal(&gshellstatus)
			if err != nil {
				return httpmock.NewStringResponse(502, "fail"), nil
			}
			return httpmock.NewStringResponse(200, string(resp)), nil
		})

	path,_ := util.GetHybridPath()
	path +=  "/instance-id"
	if util.CheckFileIsExist(path) {
		os.Remove(path)
	}

	tempfile, _ := util.GetCurrentPath()
	tempfile += "temp"
	err := util.WriteStringToFile(tempfile, mockRegion)
	// _, err := os.Create(tempfile)
	if err != nil {
		fmt.Println(err.Error())
	}
	f, e := os.OpenFile(tempfile, os.O_RDWR, 0666)
	time.Sleep(time.Duration(200) * time.Millisecond)
	guard := monkey.Patch(os.OpenFile, func(name string, flag int, perm os.FileMode) (*os.File, error)  {
		return f, e
	})
	defer func(){
		guard.Unpatch()
		if util.CheckFileIsExist(tempfile) {
			os.Remove(tempfile)
		}
	}()

	TryStartGshellChannel()
	err = InitChannelMgr(OnRecvMsg)
	assert.Equal(t, nil, err)
	assert.NotEqual(t, 0, len(G_ChannelMgr.AllChannel))
	time.Sleep(time.Duration(2) * time.Second)
	G_ChannelMgr.Uninit()

	_gshellChannel.StartChannel()
	if gshell, ok := _gshellChannel.(*GshellChannel); ok {
		gshell.SwitchChannel()
	}
	_gshellChannel.StopChannel()
}

func TestWSChannel(t *testing.T) {
	channel := NewWebsocketChannel(OnRecvMsg)
	channel.IsSupported()

	path, _ := util.GetHybridPath()
	machine_path := path + "/machine-id"
	instance_path := path + "/instance-id"
	util.WriteStringToFile(machine_path, "machine-id")
	util.WriteStringToFile(instance_path, "instance-id")
	defer func() {
		if util.CheckFileIsExist(machine_path) {
			os.Remove(machine_path)
		}
		if util.CheckFileIsExist(instance_path) {
			os.Remove(instance_path)
		}
	}()

	channel.StartChannel()
	if wschannel, ok := channel.(*WebSocketChannel); ok {
		wschannel.SwitchChannel()
		wschannel.StartPings(time.Duration(100) * time.Millisecond)
	}
	time.Sleep(time.Duration(500) * time.Microsecond)
	channel.StopChannel()
}