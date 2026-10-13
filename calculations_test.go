package main

import (
	"encoding/csv"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestLab7KnownTimings(t *testing.T) {
	cases := []struct {
		name                      string
		cpus, tasks, commands, io int
		ticks                     uint64
		mono, multi, ready        []uint64
		performance               float64
	}{
		{"one ADD EXIT", 1, 1, 2, 0, 2, []uint64{2}, []uint64{2}, []uint64{0}, 100},
		{"one IO EXIT", 1, 1, 2, 100, 5, []uint64{5}, []uint64{5}, []uint64{0}, 100},
		{"two IO EXIT", 1, 1, 3, 100, 9, []uint64{9}, []uint64{9}, []uint64{0}, 100},
		{"two tasks IO", 1, 2, 2, 100, 6, []uint64{5, 5}, []uint64{5, 6}, []uint64{0, 1}, 1000.0 / 6},
		{"three CPU", 3, 3, 2, 0, 2, []uint64{2, 2, 2}, []uint64{2, 2, 2}, []uint64{0, 0, 0}, 300},
		{"one CPU queue", 1, 3, 2, 0, 6, []uint64{2, 2, 2}, []uint64{2, 4, 6}, []uint64{0, 2, 4}, 100},
		{"one CPU overlaps IO", 1, 3, 2, 100, 7, []uint64{5, 5, 5}, []uint64{5, 6, 7}, []uint64{0, 1, 2}, 1500.0 / 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := statisticsFixture(tc.commands)
			s.CPUs = s.CPUs[:tc.cpus]
			s.IOProbability = tc.io
			for i := 1; i < tc.tasks; i++ {
				addProcess(s, i, tc.commands)
				s.RegisterTask(i)
			}
			for i := uint64(0); i < tc.ticks; i++ {
				s.StepSimulation()
				for slot, p := range s.ProcessTable {
					if p.State != StateAbsent && s.Metrics[slot].MultiTicks != s.GlobalPC-s.Metrics[slot].LoadedTick {
						t.Fatal("iterative time differs from residence")
					}
				}
			}
			if len(s.Measurements) != tc.tasks {
				t.Fatalf("completed: %d", len(s.Measurements))
			}
			for i, m := range s.Measurements {
				if !m.Success || m.Mono != tc.mono[i] || m.Multi != tc.multi[i] || m.Ready != tc.ready[i] || m.Mono != m.Commands+m.IO || m.Multi != m.FinishedTick-m.LoadedTick {
					t.Fatalf("measurement %d: %+v", i, m)
				}
			}
			if math.Abs(s.Performance().Relative-tc.performance) > 1e-9 {
				t.Fatalf("performance: %+v", s.Performance())
			}
			t.Logf("ticks=%d measurements=%+v performance=%+v", s.GlobalPC, s.Measurements, s.Performance())
		})
	}
}

func TestLab7ReadyIncrementAndEmpty(t *testing.T) {
	s := statisticsFixture(2)
	addProcess(s, 1, 2)
	s.RegisterTask(1)
	s.Process(1, StateBlocked)
	s.IncreaseReadyProcessTime(4)
	if s.Metrics[0].ReadyTicks != 4 || s.Metrics[1].ReadyTicks != 0 {
		t.Fatal("ready-only increment")
	}
	if s.Performance().Relative != 0 || executionIncrease(0, 5) != 0 {
		t.Fatal("zero denominator")
	}
	if executionIncrease(5, 2) != -60 {
		t.Fatal("unsigned subtraction")
	}
}

func TestLab7InterruptWaitExcludedFromMono(t *testing.T) {
	s := statisticsFixture(2)
	s.IOProbability = 100
	s.StepSimulation()
	s.TGlob = 10
	s.GlobalOwner = 2
	s.GlobalRemaining = 10
	for i := 0; i < 15 && s.Stats.Completed == 0; i++ {
		s.StepSimulation()
	}
	if len(s.Measurements) != 1 {
		t.Fatal("task did not finish")
	}
	m := s.Measurements[0]
	if m.Mono != 5 || m.IO != 3 || m.Multi <= 5 || m.Blocked <= 3 {
		t.Fatalf("IRQ waiting charged to mono: %+v", m)
	}
}

func TestLab7FailureReuseAndCSV(t *testing.T) {
	s := statisticsFixture(2)
	s.RemoveProcess(0) // Удаление до EXIT: не входит в успешную выборку.
	if s.Performance().Completed != 0 || s.Stats.MonoSum != 0 {
		t.Fatal("failure included")
	}
	addProcess(s, 0, 2)
	s.ProcessTable[0].TaskID = 77
	s.RegisterTask(0)
	s.StepSimulation()
	s.StepSimulation()
	if len(s.Measurements) != 2 || s.Measurements[0].Success || s.Measurements[1].TaskID != 77 || s.Stats.MonoSum != 2 {
		t.Fatal("reuse corrupted measurements")
	}
	p := filepath.Join(t.TempDir(), "results.csv")
	if err := s.ExportMeasurements(p); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil || len(rows) != 3 || rows[2][0] != "77" || rows[2][4] != "2" {
		t.Fatalf("CSV: %v %v", rows, err)
	}
}
