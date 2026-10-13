package main

import (
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testParameterText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("params.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLab8Ranges(t *testing.T) {
	cases := []ParameterRange{{100, 500, 50}, {100, 499, 50}, {0, 65535, 1}, {65535, 65535, 0}, {7, 10, 100}, {500, 100, 50}}
	for _, raw := range cases {
		r, err := raw.Normalize()
		if err != nil {
			t.Fatal(err)
		}
		a, b := rand.New(rand.NewSource(25)), rand.New(rand.NewSource(25))
		seen := map[int]bool{}
		for i := 0; i < 4096; i++ {
			v := GenerateParameterValue(r, a)
			if v != GenerateParameterValue(r, b) || v < r.Min || v > r.Max || (r.Min != r.Max && (v-r.Min)%r.Step != 0) {
				t.Fatalf("range %+v: %d", r, v)
			}
			seen[v] = true
		}
		if r.Min == 100 && r.Max == 500 && (!seen[100] || !seen[500]) {
			t.Fatal("inclusive endpoints missing")
		}
		if r.Min == 100 && r.Max == 499 && (seen[499] || !seen[450]) {
			t.Fatal("misaligned maximum")
		}
		t.Logf("range=%+v unique=%d", r, len(seen))
	}
	for _, r := range []ParameterRange{{0, 1, 0}, {-1, 2, 1}, {0, 65536, 1}, {0, 1, 65536}} {
		if _, err := r.Normalize(); err == nil {
			t.Fatalf("accepted invalid range %+v", r)
		}
	}
}

func TestLab8ParserValidation(t *testing.T) {
	base := testParameterText(t)
	for _, tc := range []struct{ name, text, want string }{
		{"missing", strings.Replace(base, "10 10 0 speed Скорость моделирования в тактах за секунду\n", "", 1), "отсутствует"},
		{"duplicate", base + "1 1 0 speed повтор\n", "повтор"},
		{"unknown", strings.Replace(base, "task_priority", "alien", 1), "неизвестный"},
		{"no comment", strings.Replace(base, "1 10 1 task_priority Базовый приоритет задания", "1 10 1 task_priority", 1), "комментарий"},
		{"non number", strings.Replace(base, "2048 2048", "oops 2048", 1), "word"},
		{"overflow", strings.Replace(base, "2048 2048", "65536 65536", 1), "word"},
		{"zero step", strings.Replace(base, "128 352 32", "128 352 0", 1), "нулевой шаг"},
		{"zero commands", strings.Replace(base, "50 140 15", "0 0 0", 1), "не меньше 1"},
		{"probability", strings.Replace(base, "10 40 10 task_io", "0 101 1 task_io", 1), "0..100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseParameterFile(strings.NewReader(tc.text))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
			t.Log(err)
		})
	}
	lines := strings.Split(strings.TrimSpace(base), "\n")
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	c, err := ParseParameterFile(strings.NewReader("\uFEFF" + strings.Join(lines, "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if c.Ranges[ParamMemory].Min != 2048 || c.Comments[ParamTaskPriority] != "Базовый приоритет задания" {
		t.Fatal("order/BOM/CRLF")
	}
	if _, err := ReadParameterFile(filepath.Join(t.TempDir(), "absent.txt")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestLab8RepeatableModelAndTaskRanges(t *testing.T) {
	c, err := ParseParameterFile(strings.NewReader(testParameterText(t)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewConfiguredSystemModel(25, c)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewConfiguredSystemModel(25, c)
	if err != nil {
		t.Fatal(err)
	}
	for tick := 0; tick < 600; tick++ {
		if a.BuildScreen() != b.BuildScreen() {
			t.Fatalf("different frames at tick %d", tick)
		}
		for _, p := range a.ProcessTable {
			if p.State == StateAbsent {
				continue
			}
			values := []int{p.Size, p.TotalCommands, p.IOPercent, p.IODuration, p.BasePriority}
			for i, v := range values {
				r := c.Ranges[int(ParamTaskSize)+i]
				if v < r.Min || v > r.Max || (v-r.Min)%r.Step != 0 {
					t.Fatalf("task out of grid %+v", p)
				}
			}
		}
		a.StepSimulation()
		b.StepSimulation()
	}
	if !reflect.DeepEqual(a.Measurements, b.Measurements) {
		t.Fatal("completion records differ")
	}
	t.Logf("seed=25 ticks=%d loaded=%d completed=%d mono_sum=%d", a.GlobalPC, a.Stats.Loaded, a.Stats.Completed, a.Stats.MonoSum)
}

func TestLab8FileReadOnceAndSystemGeneration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "params.txt")
	if err := os.WriteFile(p, []byte(testParameterText(t)), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := ReadParameterFile(p)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewConfiguredSystemModel(25, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("invalid changed file"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := NewConfiguredSystemModel(25, c)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if !reflect.DeepEqual(a.GenerateTask(), b.GenerateTask()) {
			t.Fatal("file modification changed stored parameters")
		}
	}
	c.Ranges[ParamMemory] = ParameterRange{1024, 2048, 256}
	c.Ranges[ParamSpeed] = ParameterRange{5, 20, 5}
	c.Ranges[ParamQuantum] = ParameterRange{2, 10, 2}
	c.Ranges[ParamGlobal] = ParameterRange{0, 4, 1}
	r := rand.New(rand.NewSource(25))
	s := GenerateSystemParameters(c, r)
	for i, v := range []int{s.Memory, s.Speed, s.Quantum, s.Global} {
		rr := c.Ranges[i]
		if v < rr.Min || v > rr.Max || (v-rr.Min)%rr.Step != 0 {
			t.Fatal("system out of grid")
		}
	}
	t.Logf("generated system=%+v; changed file was not reread", s)
}

func TestLab8PerTaskIO(t *testing.T) {
	s := statisticsFixture(2)
	s.IOProbability = 0
	s.NInOut = 99
	p := &s.ProcessTable[0]
	p.HasTaskParameters = true
	p.IOPercent = 100
	p.IODuration = 2
	s.StepSimulation()
	if s.IODevices[0].Remaining != 2 || p.LastCommand.Code != OpIO {
		t.Fatal("global IO parameters used")
	}
	for i := 0; i < 3; i++ {
		s.StepSimulation()
	}
	if len(s.Measurements) != 1 || s.Measurements[0].Mono != 4 || s.Measurements[0].Multi != 4 {
		t.Fatalf("per task duration: %+v", s.Measurements)
	}
}
