package main

import (
	"math/rand"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func statisticsFixture(total int) *SystemModel {
	s := fixture()
	s.Stats = ModelStatistics{}
	s.Metrics = [16]ProcessMetrics{}
	addProcess(s, 0, total)
	s.RegisterTask(0)
	return s
}

func TestStatisticsCompletion(t *testing.T) {
	s := statisticsFixture(2)
	s.StepSimulation()
	s.StepSimulation()
	if s.Stats.Completed != 1 || s.Stats.Failed != 0 || s.Stats.ExecutionSum != 2 || s.Stats.TurnaroundSum != 2 || s.Stats.ResponseSum != 1 || s.Stats.WaitSum != 0 {
		t.Fatalf("wrong timing: %+v", s.Stats)
	}
	if s.Stats.CPUs[0].BusyTicks != 2 || s.Stats.CPUs[0].Commands != 2 {
		t.Fatal("CPU utilization")
	}
	if s.Metrics[0].TaskID != 0 {
		t.Fatal("completed metrics not cleared")
	}
}

func TestStatisticsIOBlocking(t *testing.T) {
	s := statisticsFixture(2)
	s.IOProbability = 100
	s.StepSimulation()
	s.IOProbability = 0
	for i := 0; i < 4; i++ {
		s.StepSimulation()
	}
	if s.Stats.Completed != 1 || s.Stats.BlockedSum != 3 || s.Stats.ExecutionSum != 2 || s.Stats.TurnaroundSum != 5 {
		t.Fatalf("IO counted as CPU or extra wait: %+v", s.Stats)
	}
	if s.Stats.CPUs[0].BusyTicks != 2 {
		t.Fatal("blocked CPU charged as useful work")
	}
}

func TestStatisticsReadyWaitingAndReuse(t *testing.T) {
	s := statisticsFixture(2)
	s.CPUs = s.CPUs[:1]
	addProcess(s, 1, 2)
	s.RegisterTask(1)
	for i := 0; i < 4; i++ {
		s.StepSimulation()
	}
	if s.Stats.Completed != 2 || s.Stats.WaitSum != 2 || s.Stats.ExecutionSum != 4 {
		t.Fatalf("ready waiting: %+v", s.Stats)
	}
	addProcess(s, 0, 2)
	s.ProcessTable[0].TaskID = 77
	s.RegisterTask(0)
	if s.Metrics[0].Executed != 0 || s.Metrics[0].TaskID != 77 || s.Stats.Loaded != 3 {
		t.Fatal("slot reuse retained old statistics")
	}
}

func TestScreenStableAndHistoryBound(t *testing.T) {
	s := statisticsFixture(100)
	for i := 1; i <= 20; i++ {
		s.RecordDispatch(1, i)
	}
	if h := s.Stats.CPUs[0].History; len(h) != 16 || h[0] != 5 || h[15] != 20 {
		t.Fatal("history must retain latest 16 assignments")
	}
	a := s.BuildScreen()
	s.LastAction = strings.Repeat("Я", 400)
	s.StepSimulation()
	b := s.BuildScreen()
	for _, screen := range []string{a, b} {
		lines := strings.Split(screen, "\n")
		if len(lines) != ScreenHeight {
			t.Fatal("frame height changed")
		}
		for _, line := range lines {
			if utf8.RuneCountInString(line) != ScreenWidth {
				t.Fatal("frame width changed")
			}
		}
		for i := 0; i < 16; i++ {
			if !strings.HasPrefix(lines[13+i], "[") {
				t.Fatal("process row missing")
			}
		}
		if strings.Contains(screen, "NaN") || strings.Contains(screen, "Inf") {
			t.Fatal("zero denominator")
		}
	}
}

func TestStatisticsGlobalWaiting(t *testing.T) {
	s := statisticsFixture(40)
	s.TGlob = 2
	s.Kvant = 1
	addProcess(s, 1, 40)
	s.RegisterTask(1)
	addProcess(s, 2, 40)
	s.RegisterTask(2)
	for i := 0; i < 30; i++ {
		s.StepSimulation()
	}
	var wait uint64
	for _, m := range s.Metrics {
		wait += m.GlobalTicks
	}
	if wait == 0 {
		t.Fatal("global wait not recorded")
	}
	for _, c := range s.Stats.CPUs {
		if c.Commands == 0 || c.BusyTicks > 30 {
			t.Fatal("invalid utilization or starvation")
		}
	}
}

func TestLab6ReportCapture(t *testing.T) {
	if os.Getenv("LAB6_CAPTURE") != "1" {
		t.Skip("report capture")
	}
	s := fixture()
	s.Stats = ModelStatistics{}
	s.Metrics = [16]ProcessMetrics{}
	s.NextTaskID = 1
	s.RNG = rand.New(rand.NewSource(25))
	s.PauseLoad = false
	s.PSW_Task = s.GenerateTask()
	for s.LoadTask() {
	}
	s.IOProbability = 30
	s.TGlob = 2
	s.Kvant = 4
	for _, tick := range []int{1, 40, 400} {
		for int(s.GlobalPC) < tick {
			s.StepSimulation()
		}
		path := map[int]string{1: ".lab6-work/start.txt", 40: ".lab6-work/progress.txt", 400: ".lab6-work/results.txt"}[tick]
		if err := os.WriteFile(path, []byte(s.BuildScreen()), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
