package taskengine

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"bou.ke/monkey"
	"github.com/aliyun/aliyun_assist_client/agent/taskengine/timermanager"
	"github.com/aliyun/aliyun_assist_client/agent/util"
	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/assert"
)

func TestEnableFetchingTask(t *testing.T) {
	res := isEnabledFetchingTask()
	assert.Equal(t, false, res)
	EnableFetchingTask()
	res = isEnabledFetchingTask()
	assert.Equal(t, true, res)
}

func mockMetrics() {
	httpmock.Activate()
	util.NilRequest.Set()
	const mockRegion = "cn-test100"
	util.MockMetaServer(mockRegion)

	httpmock.RegisterResponder("POST",
		fmt.Sprintf("https://%s.axt.aliyun.com/luban/api/metrics", mockRegion),
		func(h *http.Request) (*http.Response, error) {
			return httpmock.NewStringResponse(200, "success"), nil
		})
}

func TestFetch(t *testing.T) {
	mockMetrics()
	defer util.NilRequest.Clear()
	defer httpmock.DeactivateAndReset()
	type args struct {
		from_kick   bool
		taskId      string
		taskType    int
		isColdstart bool
	}
	tests := []struct {
		name                  string
		args                  args
		want                  int
		isEnabledFetchingTask bool
		lockFetchingTaskLock  bool
	}{
		{
			name: "disableFetchingTask",
			args: args{},
			want: 0,
		},
		{
			name: "FetchingTaskLock.TryLockWithTimeout",
			args: args{},
			want: ErrUpdatingProcedureRunning,
		},
		{
			name: "from_kick",
			args: args{
				from_kick: true,
			},
			want: 10,
		},
		{
			name: "from_kick",
			args: args{
				from_kick: false,
			},
			want: 10,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name != "disableFetchingTask" {
				EnableFetchingTask()
			}
			if tt.name == "FetchingTaskLock.TryLockWithTimeout" {
				FetchingTaskLock.Lock()
				defer FetchingTaskLock.Unlock()
			}
			if tt.name == "from_kick" {
				guard := monkey.Patch(fetchTasks, func(reason FetchReason, taskId string, taskType int, isColdstart bool) int {
					return 10
				})
				defer guard.Unpatch()
			}
			if got := Fetch(tt.args.from_kick, tt.args.taskId, tt.args.taskType, tt.args.isColdstart); got != tt.want {
				t.Errorf("Fetch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_fetchTasks(t *testing.T) {
	mockMetrics()
	defer util.NilRequest.Clear()
	defer httpmock.DeactivateAndReset()
	type args struct {
		reason      FetchReason
		taskId      string
		taskType    int
		isColdstart bool
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{
			name: "normal",
			args: args{},
			want: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "normal" {
				monkey.Patch(FetchTaskList, func(reason FetchReason, taskId string, taskType int, isColdstart bool) *taskCollection {
					return &taskCollection{
						runInfos:     []RunTaskInfo{RunTaskInfo{}},
						stopInfos:    []RunTaskInfo{RunTaskInfo{}},
						testInfos:    []RunTaskInfo{RunTaskInfo{}},
						sendFiles:    []SendFileTaskInfo{SendFileTaskInfo{}},
						sessionInfos: []SessionTaskInfo{SessionTaskInfo{}},
					}
				})
			}
			if got := fetchTasks(tt.args.reason, tt.args.taskId, tt.args.taskType, tt.args.isColdstart); got != tt.want {
				t.Errorf("fetchTasks() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_dispatchRunTask(t *testing.T) {
	mockMetrics()
	defer util.NilRequest.Clear()
	defer httpmock.DeactivateAndReset()
	type args struct {
		taskInfo RunTaskInfo
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "taskHasExist",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
				},
			},
		},
		{
			name: "taskRepeatOnce",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
					Repeat: RunTaskOnce,
				},
			},
		},
		{
			name: "taskPeriod",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
					Repeat: RunTaskCron,
				},
			},
		},
		{
			name: "taskUnknown",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
					Repeat: RunTaskRepeatType("unknown"),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "taskHasExist" {
				taskFactory := GetTaskFactory()
				task := &Task{
					taskInfo: tt.args.taskInfo,
				}
				taskFactory.AddTask(task)
				defer taskFactory.RemoveTaskByName(tt.args.taskInfo.TaskId)
			} else if tt.name == "taskRepeatOnce" {
				var t *Task
				guard := monkey.PatchInstanceMethod(reflect.TypeOf(t), "Run", func(*Task) (presetWrapErrorCode, error) {
					return wrapErrUnknownCommandType, errors.New("some error")
				})
				defer guard.Unpatch()
			}
			dispatchRunTask(tt.args.taskInfo)
		})
	}
}

func Test_dispatchStopTask(t *testing.T) {
	mockMetrics()
	defer util.NilRequest.Clear()
	defer httpmock.DeactivateAndReset()
	type args struct {
		taskInfo RunTaskInfo
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "taskHasExist",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
					Repeat: RunTaskOnce,
				},
			},
		},
		{
			name: "taskRepeatOnce",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
					Repeat: RunTaskOnce,
				},
			},
		},
		{
			name: "taskPeriod",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
					Repeat: RunTaskCron,
				},
			},
		},
		{
			name: "taskUnknown",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
					Repeat: RunTaskRepeatType("unknown"),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "taskHasExist" {
				taskFactory := GetTaskFactory()
				task := &Task{
					taskInfo: tt.args.taskInfo,
				}
				taskFactory.AddTask(task)
				defer taskFactory.RemoveTaskByName(tt.args.taskInfo.TaskId)
			} else if tt.name == "taskRepeatOnce" {
				var t *Task
				guard := monkey.PatchInstanceMethod(reflect.TypeOf(t), "Cancel", func(*Task) {})
				defer guard.Unpatch()
			}
			dispatchStopTask(tt.args.taskInfo)
		})
	}
}

func Test_dispatchTestTask(t *testing.T) {
	mockMetrics()
	defer util.NilRequest.Clear()
	defer httpmock.DeactivateAndReset()
	type args struct {
		taskInfo RunTaskInfo
	}
	tests := []struct {
		name string
		args args
	}{{
		name: "taskHasExist",
		args: args{
			taskInfo: RunTaskInfo{
				TaskId: "abc",
				Repeat: RunTaskOnce,
			},
		},
	},
		{
			name: "taskRepeatOnce",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
					Repeat: RunTaskOnce,
				},
			},
		},
		{
			name: "taskUnknown",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
					Repeat: RunTaskRepeatType("unknown"),
				},
			},
		}, // TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "taskHasExist" {
				taskFactory := GetTaskFactory()
				task := &Task{
					taskInfo: tt.args.taskInfo,
				}
				taskFactory.AddTask(task)
				defer taskFactory.RemoveTaskByName(tt.args.taskInfo.TaskId)
			} else if tt.name == "taskRepeatOnce" {
				var t *Task
				guard := monkey.PatchInstanceMethod(reflect.TypeOf(t), "PreCheck", func(*Task, bool) error { return nil })
				defer guard.Unpatch()
			}
			dispatchTestTask(tt.args.taskInfo)
		})
	}
}

func TestPeriodicTaskSchedule_startExclusiveInvocation(t *testing.T) {
	mockMetrics()
	defer util.NilRequest.Clear()
	defer httpmock.DeactivateAndReset()
	type fields struct {
		timer              *timermanager.Timer
		reusableInvocation *Task
	}
	tests := []struct {
		name   string
		fields fields
	}{
		{
			name: "taskExist",
			fields: fields{
				timer: nil,
				reusableInvocation: &Task{
					taskInfo: RunTaskInfo{
						TaskId: "abc",
					},
				},
			},
		},
		{
			name: "normal",
			fields: fields{
				timer: nil,
				reusableInvocation: &Task{
					taskInfo: RunTaskInfo{
						TaskId: "abc",
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "taskExist" {
				taskFactory := GetTaskFactory()
				task := &Task{
					taskInfo: tt.fields.reusableInvocation.taskInfo,
				}
				taskFactory.AddTask(task)
				defer taskFactory.RemoveTaskByName(tt.fields.reusableInvocation.taskInfo.TaskId)
			} else if tt.name == "normal" {
				var t *Task
				guard := monkey.PatchInstanceMethod(reflect.TypeOf(t), "Run", func(*Task) (presetWrapErrorCode, error) {
					return wrapErrUnknownCommandType, errors.New("some error")
				})
				defer guard.Unpatch()
			}
			s := &PeriodicTaskSchedule{
				timer:              tt.fields.timer,
				reusableInvocation: tt.fields.reusableInvocation,
			}
			s.startExclusiveInvocation()
		})
	}
}

func Test_schedulePeriodicTask(t *testing.T) {
	mockMetrics()
	defer util.NilRequest.Clear()
	defer httpmock.DeactivateAndReset()
	type args struct {
		taskInfo RunTaskInfo
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name:    "TimerManagerNotInitialized",
			wantErr: true,
		},
		{
			name: "taskExist",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
				},
			},
			wantErr: false,
		},
		{
			name: "normal",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
					Cronat: "0 0 0 1 1 1",
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "TimerManagerNotInitialized" {
				guard := monkey.Patch(timermanager.GetTimerManager, func() *timermanager.TimerManager { return nil })
				defer guard.Unpatch()
			} else if tt.name == "taskExist" {
				timermanager.InitTimerManager()
				_periodicTaskSchedulesLock.Lock()
				_periodicTaskSchedules[tt.args.taskInfo.TaskId] = &PeriodicTaskSchedule{
					timer: nil,
					reusableInvocation: &Task{
						taskInfo: tt.args.taskInfo,
					},
				}
				_periodicTaskSchedulesLock.Unlock()
				defer func() {
					_periodicTaskSchedulesLock.Lock()
					delete(_periodicTaskSchedules, tt.args.taskInfo.TaskId)
					_periodicTaskSchedulesLock.Unlock()
				}()
			} else if tt.name == "normal" {
				timermanager.InitTimerManager()
				var t *timermanager.Timer
				guard := monkey.PatchInstanceMethod(reflect.TypeOf(t), "Run", func(*timermanager.Timer) (*timermanager.Timer, error) { return nil, errors.New("some error") })
				defer guard.Unpatch()
			}
			if err := schedulePeriodicTask(tt.args.taskInfo); (err != nil) != tt.wantErr {
				t.Errorf("schedulePeriodicTask() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_cancelPeriodicTask(t *testing.T) {
	mockMetrics()
	defer util.NilRequest.Clear()
	defer httpmock.DeactivateAndReset()
	type args struct {
		taskInfo RunTaskInfo
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name:    "TimerManagerNotInitialized",
			wantErr: true,
		},
		{
			name: "taskNotExist",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
				},
			},
			wantErr: true,
		},
		{
			name: "cancleTask",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
				},
			},
			wantErr: false,
		},
		{
			name: "noNeedCancelTask",
			args: args{
				taskInfo: RunTaskInfo{
					TaskId: "abc",
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "TimerManagerNotInitialized" {
				guard := monkey.Patch(timermanager.GetTimerManager, func() *timermanager.TimerManager { return nil })
				defer guard.Unpatch()
			} else if tt.name == "taskNotExist" {
				timermanager.InitTimerManager()
			} else if tt.name == "cancleTask" {
				timermanager.InitTimerManager()
				_periodicTaskSchedulesLock.Lock()
				timerManager := timermanager.GetTimerManager()
				timer, _ := timerManager.CreateCronTimer(func() {}, "0 0 0 1 1 1")
				_periodicTaskSchedules[tt.args.taskInfo.TaskId] = &PeriodicTaskSchedule{
					timer: timer,
					reusableInvocation: &Task{
						taskInfo: tt.args.taskInfo,
					},
				}
				_periodicTaskSchedulesLock.Unlock()
				defer func() {
					_periodicTaskSchedulesLock.Lock()
					delete(_periodicTaskSchedules, tt.args.taskInfo.TaskId)
					_periodicTaskSchedulesLock.Unlock()
				}()
				GetTaskFactory().AddTask(&Task{
					taskInfo: tt.args.taskInfo,
				})
				defer GetTaskFactory().RemoveTaskByName(tt.args.taskInfo.TaskId)
				var t *Task
				guard := monkey.PatchInstanceMethod(reflect.TypeOf(t), "Cancel", func(*Task) {})
				defer guard.Unpatch()
			} else if tt.name == "noNeedCancelTask" {
				timermanager.InitTimerManager()
				_periodicTaskSchedulesLock.Lock()
				timerManager := timermanager.GetTimerManager()
				timer, _ := timerManager.CreateCronTimer(func() {}, "0 0 0 1 1 1")
				_periodicTaskSchedules[tt.args.taskInfo.TaskId] = &PeriodicTaskSchedule{
					timer: timer,
					reusableInvocation: &Task{
						taskInfo: tt.args.taskInfo,
					},
				}
				_periodicTaskSchedulesLock.Unlock()
				defer func() {
					_periodicTaskSchedulesLock.Lock()
					delete(_periodicTaskSchedules, tt.args.taskInfo.TaskId)
					_periodicTaskSchedulesLock.Unlock()
				}()
				guard := monkey.Patch(util.HttpPost, func(string, string, string) (string, error) { return "", nil })
				defer guard.Unpatch()
			}
			if err := cancelPeriodicTask(tt.args.taskInfo); (err != nil) != tt.wantErr {
				t.Errorf("cancelPeriodicTask() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
