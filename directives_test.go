package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func directiveOK(t *testing.T, s *SystemModel, text string) {
	t.Helper()
	if err := s.ExecuteDirective(text); err != nil {
		t.Fatal(text, err)
	}
}
func operatorFixture(total int) *SystemModel {
	s := statisticsFixture(total)
	s.TGlob = 0
	s.PSW_Task = PSW{TaskID: 100, Size: 3, TotalCommands: 2, IOPercent: 0, IODuration: 2, HasTaskParameters: true, BasePriority: 5, State: StateAbsent}
	s.NextTaskID = 101
	return s
}
func TestLab9TaskAtomicAndManualLoad(t *testing.T) {
	s := operatorFixture(100)
	before := s.PSW_Task
	for _, text := range []string{"task size=10 io=101", "task size=0", "task commands=-1", "task size=8 size=9", "task wrong=3", "task"} {
		if s.ExecuteDirective(text) == nil || !reflect.DeepEqual(before, s.PSW_Task) {
			t.Fatal("invalid task changed", text)
		}
	}
	directiveOK(t, s, "task size=20 commands=7 io=100 duration=4 priority=0")
	if s.PSW_Task.TaskID != 100 || s.PSW_Task.Size != 20 || s.PSW_Task.TotalCommands != 7 || s.PSW_Task.IOPercent != 100 || s.PSW_Task.IODuration != 4 || s.PSW_Task.BasePriority != 0 {
		t.Fatal(s.PSW_Task)
	}
	directiveOK(t, s, "pause-load")
	if s.LoadTask() {
		t.Fatal("automatic load ignored pause")
	}
	directiveOK(t, s, "load")
	if !s.PauseLoad || s.ProcessTable[1].TaskID != 100 || s.PSW_Task.TaskID != 101 || s.Stats.Loaded != 2 {
		t.Fatal("manual load must load exactly one")
	}
	directiveOK(t, s, "resume-load")
	if s.PauseLoad {
		t.Fatal("resume load")
	}
}
func TestLab9TaskIDAndMemory(t *testing.T) {
	s := operatorFixture(10)
	s.ProcessTable[0].TaskID = 77
	s.Metrics[0].TaskID = 77
	if s.ExecuteDirective("suspend 1") == nil {
		t.Fatal("slot accepted as ID")
	}
	directiveOK(t, s, "suspend 77")
	if !s.ProcessTable[0].Suspended {
		t.Fatal("ID selection")
	}
	beforeTotal, beforeFree := s.TotalMemory, s.FreeMemory
	if s.ExecuteDirective("memory 2") == nil || s.TotalMemory != beforeTotal || s.FreeMemory != beforeFree {
		t.Fatal("shrunk occupied memory")
	}
	directiveOK(t, s, "memory 3")
	if s.TotalMemory != 3 || s.FreeMemory != 0 {
		t.Fatal("memory invariant")
	}
	directiveOK(t, s, "memory 4096")
	if s.FreeMemory != 4093 {
		t.Fatal("memory resize")
	}
}
func TestLab9SuspendActiveAndBlocked(t *testing.T) {
	s := operatorFixture(4)
	s.StepSimulation()
	directiveOK(t, s, "suspend 1")
	for i := 0; i < 5; i++ {
		s.StepSimulation()
	}
	if s.ProcessTable[0].PC != 1 || s.Metrics[0].Executed != 1 || s.Metrics[0].SuspendedTicks != 5 || s.QueueReady() != 0 {
		t.Fatal("held process executed or counted as ready")
	}
	directiveOK(t, s, "resume 1")
	directiveOK(t, s, "finish")
	if err := s.RunBatch(100, nil); err != nil {
		t.Fatal(err)
	}
	if s.IsRunning || s.Stats.Completed != 1 || s.Stats.Failed != 0 {
		t.Fatal("resume active")
	}
	s = operatorFixture(2)
	s.IOProbability = 100
	s.NInOut = 2
	s.StepSimulation()
	if s.ProcessTable[0].State != StateBlocked {
		t.Fatal("not blocked")
	}
	directiveOK(t, s, "suspend 1")
	for i := 0; i < 5; i++ {
		s.StepSimulation()
	}
	if s.ProcessTable[0].PC != 1 || s.ProcessTable[0].State != StateReady || !s.ProcessTable[0].Suspended || s.IODevices[0].Active {
		t.Fatal("IO must complete while held")
	}
	directiveOK(t, s, "finish")
	_ = s.RunBatch(100, nil)
	if s.IsRunning || len(s.Measurements) != 1 || s.Measurements[0].Commands != 2 {
		t.Fatal("finish must resume held task")
	}
}
func (s *SystemModel) QueueReady() int { r, _ := s.QueueLengths(); return r }
func TestLab9DeferredServiceSurvivesSuspend(t *testing.T) {
	for _, action := range []string{"IO", "EXIT"} {
		t.Run(action, func(t *testing.T) {
			total := 2
			if action == "EXIT" {
				total = 1
			}
			s := operatorFixture(total)
			s.DispatchReadyProcesses()
			cpu := &s.CPUs[0]
			s.TGlob = 3
			s.GlobalOwner = 2
			s.GlobalRemaining = 3
			if action == "IO" {
				s.IOProbability = 100
				s.NInOut = 1
			}
			s.ExecuteCommand(cpu)
			if cpu.PendingAction != action {
				t.Fatalf("expected pending %s, got %s", action, cpu.PendingAction)
			}
			directiveOK(t, s, "suspend 1")
			if s.ProcessTable[0].DeferredAction != action || s.ProcessTable[0].PC != 1 || len(s.GlobalWaiters) != 0 {
				t.Fatal("lost service or waiter")
			}
			directiveOK(t, s, "resume 1")
			directiveOK(t, s, "finish")
			_ = s.RunBatch(100, nil)
			if s.IsRunning || len(s.Measurements) != 1 || s.Measurements[0].Commands != uint64(total) || !s.Measurements[0].Success {
				t.Fatalf("service repeated/lost: %+v", s.Measurements)
			}
		})
	}
}
func TestLab9KillCleansReferences(t *testing.T) {
	for _, state := range []string{"active", "blocked", "system", "irq", "exit"} {
		t.Run(state, func(t *testing.T) {
			s := operatorFixture(10)
			s.DispatchReadyProcesses()
			if state == "blocked" || state == "system" || state == "irq" {
				s.IOProbability = 100
				s.ExecuteCommand(&s.CPUs[0])
				if state != "system" {
					s.RunSystemProcesses()
				}
			}
			if state == "irq" {
				s.IODevices[0].Active = false
				s.IODevices[0].InterruptPending = true
				s.RaiseInterrupt(Interrupt{Kind: InterruptIO, ProcessSlot: 0, TaskID: 1, DeviceID: 1, CPUID: 1})
			}
			if state == "exit" {
				s.ProcessTable[0].LastCommand.Code = OpExit
				s.CPUs[0].PendingAction = "EXIT"
			}
			if s.CPUs[0].IsBusy {
				s.CPUs[0].WaitingGlobal = true
				s.GlobalWaiters = []int{1}
			}
			directiveOK(t, s, "kill 1")
			if !s.AllTasksCompleted() || s.FreeMemory != s.TotalMemory || len(s.Interrupts) != 0 || len(s.GlobalWaiters) != 0 || s.IODevices[0].Active || s.IODevices[0].InterruptPending {
				t.Fatal("dangling reference")
			}
			if s.Stats.Cancelled != 1 || s.Stats.Failed != 1 || len(s.Measurements) != 1 || s.Measurements[0].Success {
				t.Fatal("kill counted as success")
			}
			for _, sp := range s.SystemProcesses {
				if sp.ParentTaskID == 1 && sp.State != StateAbsent {
					t.Fatal("orphan service")
				}
			}
			directiveOK(t, s, "load")
			if s.ProcessTable[0].TaskID != 100 {
				t.Fatal("slot reuse")
			}
			s.HandleInterrupts()
			s.RunSystemProcesses()
			if s.ProcessTable[0].TaskID != 100 || s.ProcessTable[0].State != StateReady {
				t.Fatal("stale reference touched reused slot")
			}
		})
	}
}
func TestLab9FinishAndValidation(t *testing.T) {
	s := operatorFixture(3)
	for _, text := range []string{"speed NaN", "speed +Inf", "speed 0", "memory 0", "suspend", "kill 99", "load extra", "unknown"} {
		if s.ExecuteDirective(text) == nil {
			t.Fatal("accepted", text)
		}
	}
	directiveOK(t, s, "speed 0.5")
	if s.Speed != .5 {
		t.Fatal(s.Speed)
	}
	directiveOK(t, s, "finish")
	for _, text := range []string{"load", "resume-load", "suspend 1"} {
		if s.ExecuteDirective(text) == nil {
			t.Fatal("accepted during finish", text)
		}
	}
	_ = s.RunBatch(100, nil)
	if s.IsRunning || s.GlobalPC != 3 || s.Stats.Loaded != 1 || s.PSW_Task.TaskID != 100 {
		t.Fatal("finish loaded prepared task or failed to stop", s.GlobalPC)
	}
	s.StepSimulation()
	if s.GlobalPC != 3 {
		t.Fatal("tick after poweroff")
	}
	s = fixture()
	directiveOK(t, s, "finish")
	if s.IsRunning {
		t.Fatal("empty finish")
	}
}
func TestLab9ScriptAndRepeatability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "script.txt")
	text := "# demonstration\n0 pause-load\n0 suspend 2\n5 resume 2\n10 kill 1\n20 finish\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	events, err := ReadDirectiveScript(path)
	if err != nil {
		t.Fatal(err)
	}
	config, err := ReadParameterFile("params.txt")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := NewConfiguredSystemModel(25, config)
	b, _ := NewConfiguredSystemModel(25, config)
	if err = a.RunBatch(5000, events); err != nil {
		t.Fatal(err)
	}
	if err = b.RunBatch(5000, events); err != nil {
		t.Fatal(err)
	}
	if a.IsRunning || a.BuildScreen() != b.BuildScreen() || !reflect.DeepEqual(a.Measurements, b.Measurements) {
		t.Fatal("script not repeatable")
	}
	for _, text := range []string{"-1 q", "2 q\n1 q", "3", "x q"} {
		_ = os.WriteFile(path, []byte(text), 0600)
		if _, err = ReadDirectiveScript(path); err == nil {
			t.Fatal("accepted bad script", text)
		}
	}
	if !strings.Contains(a.BuildScreen(), "питание=false") {
		t.Fatal("finish status missing")
	}
}
func TestLab9ConcurrentOperator(t *testing.T) {
	s := operatorFixture(10000)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			s.StepSimulation()
			_ = s.BuildScreen()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = s.ExecuteDirective("suspend 1")
			_ = s.ExecuteDirective("resume 1")
			_ = s.ExecuteDirective("memory 4096")
			_ = s.ExecuteDirective("speed 10")
		}
	}()
	wg.Wait()
}
