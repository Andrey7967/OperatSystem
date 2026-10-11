package main

import (
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"testing"
)

func startTestIO(sys *SystemModel, slot int, direction IODirection) {
	cpu := sys.CPUForProcess(slot)
	p := &sys.ProcessTable[slot]
	p.LastCommand = Command{Code: OpIO, Direction: direction, IOCount: 3}
	cpu.LastCommand = OpIO
	sys.InitializeIO(cpu)
	sys.RunSystemProcesses()
}

func TestIOTransfersAndInterruptSeparation(t *testing.T) {
	for _, direction := range []IODirection{IORead, IOWrite} {
		sys := fixture()
		addProcess(sys, 0, 10)
		sys.DispatchReadyProcesses()
		startTestIO(sys, 0, direction)
		for tick := 1; tick <= 3; tick++ {
			sys.GlobalPC++
			sys.StepIO()
			device := sys.IODevices[0]
			if device.Transferred != tick || device.Remaining != 3-tick {
				t.Fatalf("device tick %d: %+v", tick, device)
			}
			if sys.ProcessTable[0].State != StateBlocked {
				t.Fatal("device changed process state without regulator/IRQ")
			}
		}
		if len(sys.Interrupts) != 1 || sys.Interrupts[0].Kind != InterruptIO || sys.HandledInterrupts != 0 {
			t.Fatal("completion did not produce pending IRQ")
		}
		if direction == IORead && !reflect.DeepEqual(sys.ProcessTable[0].Memory, []int{100, 101, 102}) {
			t.Fatal("input was not copied to memory")
		}
		if direction == IOWrite && !reflect.DeepEqual(sys.IODevices[0].Buffer, []int{10, 20, 30}) {
			t.Fatal("output was not copied to device")
		}
		signal := sys.Interrupts[0]
		sys.HandleInterrupts()
		if sys.ProcessTable[0].State != StateReady || sys.IODevices[0].InterruptPending || sys.HandledInterrupts != 1 {
			t.Fatal("IRQ did not unblock process")
		}
		sys.RaiseInterrupt(signal)
		sys.HandleInterrupts()
		if sys.HandledInterrupts != 1 {
			t.Fatal("duplicate IRQ processed")
		}
	}
}

func TestIOCopiesAllWordsWithShortDuration(t *testing.T) {
	sys := fixture()
	sys.NInOut = 1
	addProcess(sys, 0, 10)
	sys.DispatchReadyProcesses()
	startTestIO(sys, 0, IORead)
	sys.StepSimulation()
	if !reflect.DeepEqual(sys.ProcessTable[0].Memory, []int{100, 101, 102}) || sys.HandledInterrupts != 1 {
		t.Fatal("short IO lost words")
	}
}

func TestStaleInterruptCannotUnblockReusedSlot(t *testing.T) {
	sys := fixture()
	addProcess(sys, 0, 10)
	sys.DispatchReadyProcesses()
	startTestIO(sys, 0, IORead)
	for tick := 0; tick < 3; tick++ {
		sys.GlobalPC++
		sys.StepIO()
	}
	sys.RemoveProcess(0)
	addProcess(sys, 0, 10)
	sys.ProcessTable[0].TaskID = 99
	sys.Process(0, StateBlocked)
	sys.HandleInterrupts()
	if sys.ProcessTable[0].State != StateBlocked || sys.HandledInterrupts != 0 {
		t.Fatal("old IRQ affected new task")
	}
}

func TestIOMemoryFaultInterrupt(t *testing.T) {
	sys := fixture()
	addProcess(sys, 0, 10)
	sys.DispatchReadyProcesses()
	startTestIO(sys, 0, IORead)
	sys.ProcessTable[0].Memory = nil
	sys.GlobalPC++
	sys.StepIO()
	if len(sys.Interrupts) != 1 || sys.Interrupts[0].Kind != InterruptFault {
		t.Fatal("IO memory error not signalled")
	}
	sys.HandleInterrupts()
	if sys.ProcessTable[0].State != StateAbsent || sys.FreeMemory != sys.TotalMemory {
		t.Fatal("fault did not release resources")
	}
}

func TestRelativePriorityAndTimerInterrupt(t *testing.T) {
	sys := fixture()
	sys.CPUs = sys.CPUs[:1]
	sys.Kvant = 3
	addProcess(sys, 0, 20)
	sys.DispatchReadyProcesses()
	addProcess(sys, 1, 20)
	sys.ProcessTable[1].BasePriority = 10
	sys.StepSimulation()
	if sys.CPUs[0].ActiveProcID != 0 {
		t.Fatal("relative priority preempted before quantum")
	}
	sys.StepSimulation()
	sys.StepSimulation()
	if sys.CPUs[0].ActiveProcID != 1 || sys.ProcessTable[0].PC != 3 || sys.ProcessTable[0].DynPriority != -1 {
		t.Fatal("priority selection at quantum")
	}
	if sys.HandledInterrupts != 1 || sys.InterruptHistory[0].Kind != InterruptTimer {
		t.Fatal("no timer interrupt")
	}
}

func TestGlobalAccessWaitAndFairness(t *testing.T) {
	sys := fixture()
	sys.TGlob = 2
	sys.Kvant = 1
	for i := 0; i < 3; i++ {
		addProcess(sys, i, 1000)
	}
	sys.StepSimulation()
	if !sys.CPUs[1].WaitingGlobal || !sys.CPUs[2].WaitingGlobal || sys.ProcessTable[1].PC != 0 {
		t.Fatal("no wait for shared tables")
	}
	for tick := 0; tick < 100; tick++ {
		sys.StepSimulation()
	}
	for i := 0; i < 3; i++ {
		if sys.ProcessTable[i].PC == 0 {
			t.Fatal("shared data access starved CPU", i)
		}
	}
}

func TestExitWaitsForGlobalAccess(t *testing.T) {
	sys := fixture()
	addProcess(sys, 0, 10)
	addProcess(sys, 1, 1)
	sys.DispatchReadyProcesses()
	sys.TGlob = 2
	sys.GlobalOwner = 1
	sys.GlobalRemaining = 2
	before := sys.FreeMemory
	sys.ExecuteCommand(&sys.CPUs[1])
	if !sys.CPUs[1].WaitingGlobal || sys.CPUs[1].PendingAction != "EXIT" || sys.FreeMemory != before {
		t.Fatal("EXIT modified locked tables")
	}
	sys.StepSimulation()
	sys.StepSimulation()
	if sys.ProcessTable[1].State != StateAbsent || sys.FreeMemory != before+3 {
		t.Fatal("deferred EXIT did not complete")
	}
}

func TestIOInterruptWaitsForGlobalAccess(t *testing.T) {
	sys := fixture()
	addProcess(sys, 0, 10)
	sys.DispatchReadyProcesses()
	startTestIO(sys, 0, IORead)
	sys.TGlob = 2
	sys.GlobalOwner = 2
	sys.GlobalRemaining = 2
	for tick := 0; tick < 3; tick++ {
		sys.GlobalPC++
		sys.StepIO()
	}
	sys.HandleInterrupts()
	if sys.ProcessTable[0].State != StateBlocked || len(sys.Interrupts) != 1 || sys.HandledInterrupts != 0 {
		t.Fatal("IRQ bypassed shared lock")
	}
	sys.StepGlobalAccess()
	sys.StepGlobalAccess()
	sys.HandleInterrupts()
	if sys.ProcessTable[0].State != StateReady || sys.HandledInterrupts != 1 {
		t.Fatal("IRQ not processed after lock release")
	}
}

func TestIdleSystemLoadsAndDispatches(t *testing.T) {
	sys := fixture()
	sys.PauseLoad = false
	sys.CheckSystemState()
	if !sys.CPUs[0].IsBusy || sys.FreeMemory == sys.TotalMemory {
		t.Fatal("idle system did not load jobs")
	}
	sys = fixture()
	sys.CheckSystemState()
	for _, cpu := range sys.CPUs {
		if cpu.IsBusy || cpu.ActiveProcID != -1 {
			t.Fatal("empty system not idle")
		}
	}
}

func TestLab5LongRunWithGlobalDelay(t *testing.T) {
	sys := NewSystemModel()
	sys.RNG = rand.New(rand.NewSource(25))
	sys.TGlob = 2
	for tick := 0; tick < 5000; tick++ {
		sys.StepSimulation()
		used := 0
		seen := map[int]bool{}
		for i, p := range sys.ProcessTable {
			if p.State == StateAbsent {
				continue
			}
			used += p.Size
			if len(p.Memory) != p.Size || p.PC > p.TotalCommands || p.PC < 0 {
				t.Fatal("process memory/PC", tick, i)
			}
			if p.State == StateBlocked {
				device := sys.IODevices[i]
				if device.TaskID != p.TaskID || (!device.Active && !device.InterruptPending) || p.IORemaining != device.Remaining {
					t.Fatal("blocked task has no IO", tick, i)
				}
			}
		}
		if used+sys.FreeMemory != sys.TotalMemory {
			t.Fatal("memory balance", tick)
		}
		for _, cpu := range sys.CPUs {
			if cpu.IsBusy {
				if cpu.ActiveProcID < 0 || seen[cpu.ActiveProcID] {
					t.Fatal("CPU ownership", tick)
				}
				seen[cpu.ActiveProcID] = true
				p := sys.ProcessTable[cpu.ActiveProcID]
				if p.State != StateActive || p.PC != cpu.PC {
					t.Fatal("CPU context", tick)
				}
			} else if cpu.ActiveProcID != -1 || cpu.PC != 0 || cpu.SystemProcID != 0 {
				t.Fatal("idle CPU context", tick)
			}
		}
		for i, p := range sys.ProcessTable {
			if (p.State == StateActive) != seen[i] {
				t.Fatal("active state ownership", tick, i)
			}
		}
	}
	if sys.HandledInterrupts == 0 || sys.CompletedSystemProcesses == 0 {
		t.Fatal("no activity in long run")
	}
}

func captureLab5(t *testing.T, sys *SystemModel, name string) {
	t.Helper()
	file, err := os.Create(".lab5-work/" + name + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = file
	sys.RenderUI()
	os.Stdout = saved
	file.Close()
}

func TestLab5ReportCapture(t *testing.T) {
	if os.Getenv("LAB5_CAPTURE") != "1" {
		t.Skip("report capture requested explicitly")
	}
	sys := fixture()
	sys.GlobalPC = 1
	for i := 0; i < 3; i++ {
		addProcess(sys, i, 12)
	}
	sys.DispatchReadyProcesses()
	for i, direction := range []IODirection{IORead, IOWrite} {
		sys.CPUs[i].PC = 1
		sys.SaveProcessState(&sys.CPUs[i])
		startTestIO(sys, i, direction)
	}
	trace, err := os.Create(".lab5-work/trace.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer trace.Close()
	fmt.Fprintln(trace, "tick,state,pc,remaining,m0,m1,m2,input_transferred,output_transferred,raised,handled")
	for tick := 1; tick <= 5; tick++ {
		if tick > 1 {
			sys.StepSimulation()
		}
		p := sys.ProcessTable[0]
		fmt.Fprintf(trace, "%d,%s,%d,%d,%d,%d,%d,%d,%d,%d,%d\n", tick, p.State, p.PC, p.IORemaining, p.Memory[0], p.Memory[1], p.Memory[2], sys.IODevices[0].Transferred, sys.IODevices[1].Transferred, sys.RaisedInterrupts, sys.HandledInterrupts)
		if tick == 1 || tick == 2 || tick == 4 || tick == 5 {
			captureLab5(t, sys, fmt.Sprintf("screen-%d", tick))
		}
	}
	sys = fixture()
	sys.TGlob = 2
	for i := 0; i < 3; i++ {
		addProcess(sys, i, 12)
	}
	sys.StepSimulation()
	captureLab5(t, sys, "global")
	sys = fixture()
	addProcess(sys, 0, 12)
	sys.DispatchReadyProcesses()
	startTestIO(sys, 0, IORead)
	for tick := 0; tick < 3; tick++ {
		sys.GlobalPC++
		sys.StepIO()
	}
	captureLab5(t, sys, "irq-pending")
	sys.HandleInterrupts()
	captureLab5(t, sys, "irq-handled")
	sys = fixture()
	sys.CPUs = sys.CPUs[:1]
	sys.Kvant = 2
	addProcess(sys, 0, 12)
	addProcess(sys, 1, 12)
	sys.StepSimulation()
	sys.StepSimulation()
	captureLab5(t, sys, "timer")
}
