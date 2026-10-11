package main

import (
	"math/rand"
	"testing"
)

func TestSystemProcessIOLifecycle(t *testing.T) {
	sys := fixture()
	addProcess(sys, 0, 10)
	sys.IOProbability = 100
	sys.DispatchReadyProcesses()
	cpu := &sys.CPUs[0]
	sys.ExecuteCommand(cpu)
	process := &sys.SystemProcesses[0]
	if process.ID == 0 || process.ParentTaskID != 1 || process.State != StateReady || process.PC != 0 {
		t.Fatalf("system process was not created: %+v", process)
	}
	if parent := sys.ProcessTable[0]; parent.State != StateInitIO || parent.PC != 1 || cpu.IsBusy {
		t.Fatalf("parent context: %+v, CPU: %+v", parent, cpu)
	}
	sys.RunSystemProcesses()
	if process.PC != 1 || process.State != StateAbsent || process.CreatedTick != process.FinishedTick || sys.CompletedSystemProcesses != 1 {
		t.Fatalf("system process did not finish in the IO request tick: %+v", process)
	}
	if sys.ProcessTable[0].State != StateBlocked || sys.ProcessTable[0].IORemaining != 3 || cpu.IsBusy || cpu.SystemProcID != 0 || cpu.PC != 0 {
		t.Fatal("IO not started or system CPU context not released")
	}
	sys.RunSystemProcesses()
	if sys.CompletedSystemProcesses != 1 {
		t.Fatal("system process executed twice")
	}
}

func TestIOExactDurationMultipleCPUs(t *testing.T) {
	for _, duration := range []int{1, 3, 5} {
		sys := fixture()
		sys.NInOut = duration
		sys.IOProbability = 100
		for i := 0; i < 3; i++ {
			addProcess(sys, i, 10)
		}
		sys.StepSimulation()
		if sys.CompletedSystemProcesses != 3 {
			t.Fatal("not all three system processes ran")
		}
		for i := 0; i < 3; i++ {
			process := sys.SystemProcesses[i]
			if process.ParentTaskID != i+1 || process.CPUID != i+1 || process.CreatedTick != 1 || process.FinishedTick != 1 {
				t.Fatalf("wrong system process binding: %+v", process)
			}
		}
		sys.IOProbability = 0
		for elapsed := 1; elapsed <= duration; elapsed++ {
			sys.StepSimulation()
			for i := 0; i < 3; i++ {
				p := sys.ProcessTable[i]
				if elapsed < duration && (p.State != StateBlocked || p.PC != 1 || p.IORemaining != duration-elapsed) {
					t.Fatalf("duration=%d elapsed=%d: %+v", duration, elapsed, p)
				}
				if elapsed == duration && (p.State != StateActive || p.PC != 2 || p.IORemaining != 0) {
					t.Fatalf("extra delay duration=%d: %+v", duration, p)
				}
			}
		}
	}
}

func fixture() *SystemModel {
	sys := NewSystemModel()
	sys.PauseLoad = true
	sys.IOProbability = 0
	sys.RNG = rand.New(rand.NewSource(1))
	for i := range sys.ProcessTable {
		sys.ProcessTable[i] = PSW{State: StateAbsent}
	}
	for i := range sys.CPUs {
		sys.CPUs[i] = CPU{ID: i + 1, ActiveProcID: -1}
	}
	sys.FreeMemory = sys.TotalMemory
	return sys
}

func addProcess(sys *SystemModel, slot, total int) {
	sys.ProcessTable[slot] = PSW{TaskID: slot + 1, Size: 3, Memory: []int{10, 20, 30},
		TotalCommands: total, BasePriority: 5, State: StateReady}
	sys.FreeMemory -= 3
}

func TestCommandPipeline(t *testing.T) {
	sys := fixture()
	addProcess(sys, 0, 5)
	sys.StepSimulation()
	p := sys.ProcessTable[0]
	if p.Memory[0] != 50 || p.PC != 1 || p.LastCommand.Code != OpCalculate {
		t.Fatalf("ADD/PC: %+v", p)
	}
	c, a, b, r := sys.DecodeCommand(p.LastCommand)
	if c != OpCalculate || a != 1 || b != 2 || r != 0 {
		t.Fatal("decode")
	}
	sys.StepSimulation()
	if sys.ProcessTable[0].Memory[1] != 80 {
		t.Fatal("second ADD")
	}
}

func TestIOContextAndDuration(t *testing.T) {
	sys := fixture()
	addProcess(sys, 0, 6)
	sys.IOProbability = 100
	sys.StepSimulation()
	if p := sys.ProcessTable[0]; p.State != StateBlocked || p.PC != 1 || p.IORemaining != 3 {
		t.Fatal("IO init", p)
	}
	for _, cpu := range sys.CPUs {
		if cpu.IsBusy {
			t.Fatal("IO occupies CPU")
		}
	}
	sys.IOProbability = 0
	for tick, remain := range []int{2, 1} {
		sys.StepSimulation()
		p := sys.ProcessTable[0]
		if p.State != StateBlocked || p.PC != 1 || p.IORemaining != remain {
			t.Fatalf("tick %d: %+v", tick+2, p)
		}
	}
	sys.StepSimulation()
	if p := sys.ProcessTable[0]; p.State != StateActive || p.PC != 2 || p.IORemaining != 0 {
		t.Fatal("restore after IO", p)
	}
}

func TestExitPrecedesIOAndReleasesMemory(t *testing.T) {
	sys := fixture()
	addProcess(sys, 0, 1)
	sys.IOProbability = 100
	sys.StepSimulation()
	if sys.ProcessTable[0].State != StateAbsent || sys.FreeMemory != sys.TotalMemory {
		t.Fatal("EXIT resources")
	}
	if sys.CPUs[0].LastCommand != OpExit {
		t.Fatal("last command must EXIT")
	}
	for _, cpu := range sys.CPUs {
		if cpu.IsBusy {
			t.Fatal("CPU still busy")
		}
	}
}

func TestReloadAfterExit(t *testing.T) {
	sys := fixture()
	addProcess(sys, 0, 1)
	sys.PauseLoad = false
	// Не допускаем периодическую загрузку на первом такте.
	sys.GlobalPC = 1
	sys.PSW_Task = PSW{TaskID: 100, Size: 128, TotalCommands: 5, BasePriority: 5, State: StateReady}
	sys.StepSimulation()
	found := false
	for _, p := range sys.ProcessTable {
		if p.TaskID == 100 {
			found = true
			if len(p.Memory) != 128 || p.PC != 0 {
				t.Fatal("new task memory/PC")
			}
		}
	}
	if !found {
		t.Fatal("no task loaded after EXIT")
	}
}

func TestMemoryProtectionAndUnknownOperation(t *testing.T) {
	sys := fixture()
	addProcess(sys, 0, 5)
	p := &sys.ProcessTable[0]
	if _, err := sys.ReadOperand(p, -1); err == nil {
		t.Fatal("negative read")
	}
	if err := sys.WriteResult(p, 3, 42); err == nil {
		t.Fatal("out of range write")
	}
	if p.Memory[0] != 10 {
		t.Fatal("invalid access changed memory")
	}
	sys.DispatchReadyProcesses()
	if _, err := sys.DoOperation(&sys.CPUs[0], OperationCode("BAD"), 0, 0); err == nil {
		t.Fatal("unknown opcode")
	}
	// Повреждённая память проходит путь удаления по ошибке в ExecuteCommand.
	p.Memory = nil
	sys.ExecuteCommand(&sys.CPUs[0])
	if sys.ProcessTable[0].State != StateAbsent || sys.FreeMemory != sys.TotalMemory {
		t.Fatal("abort resources")
	}
}

func TestQuantumAndSingleCommand(t *testing.T) {
	sys := fixture()
	sys.Kvant = 1
	addProcess(sys, 0, 100)
	for tick := 1; tick <= 20; tick++ {
		sys.StepSimulation()
		if sys.ProcessTable[0].PC != tick {
			t.Fatal("multiple execution in same tick")
		}
	}
	sys = fixture()
	sys.CPUs = sys.CPUs[:1]
	addProcess(sys, 0, 100)
	addProcess(sys, 1, 100)
	for tick := 0; tick < 10; tick++ {
		sys.StepSimulation()
	}
	if sys.ProcessTable[0].PC != 10 || sys.ProcessTable[0].DynPriority != -1 || sys.CPUs[0].ActiveProcID != 1 {
		t.Fatal("quantum priority switch")
	}
}

func TestSimulationInvariants(t *testing.T) {
	sys := NewSystemModel()
	sys.RNG = rand.New(rand.NewSource(25))
	for tick := 0; tick < 3000; tick++ {
		sys.StepSimulation()
		used := 0
		seen := map[int]bool{}
		for slot, p := range sys.ProcessTable {
			if p.State != StateAbsent {
				used += p.Size
				if len(p.Memory) != p.Size || p.PC >= p.TotalCommands || p.PC < 0 {
					t.Fatalf("memory/PC tick=%d slot=%d", tick, slot)
				}
				if p.State == StateBlocked || p.State == StateInitIO {
					if p.IORemaining <= 0 {
						t.Fatal("IO counter")
					}
				}
			}
		}
		if used+sys.FreeMemory != sys.TotalMemory || sys.FreeMemory < 0 {
			t.Fatal("memory accounting")
		}
		for _, cpu := range sys.CPUs {
			if cpu.IsBusy {
				if seen[cpu.ActiveProcID] {
					t.Fatal("duplicate assignment")
				}
				seen[cpu.ActiveProcID] = true
				p := sys.ProcessTable[cpu.ActiveProcID]
				if p.State != StateActive || p.PC != cpu.PC {
					t.Fatal("CPU/PSW mismatch")
				}
			} else if cpu.ActiveProcID != -1 || cpu.PC != 0 {
				t.Fatal("idle context")
			}
		}
		for slot, p := range sys.ProcessTable {
			if (p.State == StateActive) != seen[slot] {
				t.Fatal("unowned active process")
			}
		}
	}
}
