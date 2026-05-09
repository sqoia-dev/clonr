package stats

import (
	"errors"
	"testing"
)

// Sprint 38 STAT-REGISTRY tests.
//
// Coverage:
//   - register + lookup roundtrip
//   - duplicate-name-different-device collision (allowed) vs. duplicate
//     (name, device) (rejected)
//   - missing-required-option validation (Title)
//   - invalid type / empty name
//   - All() ordering
//   - Sample() ergonomic helper

func TestMetricRegistry_RegisterAndLookup(t *testing.T) {
	r := NewMetricRegistry()

	d, err := r.Register(TypeFloat, "ib_rate_gbps",
		Device("mlx5_0/1"),
		Unit("gbps"),
		Upper(200),
		Title("InfiniBand link rate"),
		ChartGroup("InfiniBand"),
	)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if d.Name != "ib_rate_gbps" {
		t.Errorf("Name = %q, want ib_rate_gbps", d.Name)
	}
	if d.Type != TypeFloat {
		t.Errorf("Type = %q, want float", d.Type)
	}
	if d.ChartGroup != "InfiniBand" {
		t.Errorf("ChartGroup = %q, want InfiniBand", d.ChartGroup)
	}

	got, ok := r.Get("ib_rate_gbps", "mlx5_0/1")
	if !ok {
		t.Fatalf("Get returned !ok for registered metric")
	}
	if got.Unit != "gbps" || got.Upper != 200 {
		t.Errorf("Get returned wrong fields: %+v", got)
	}

	// Wrong device: lookup miss.
	if _, ok := r.Get("ib_rate_gbps", "nope"); ok {
		t.Errorf("Get unexpectedly hit for wrong device")
	}
}

func TestMetricRegistry_DuplicateSameNameDifferentDevice(t *testing.T) {
	r := NewMetricRegistry()

	if _, err := r.Register(TypeFloat, "rate_gbps",
		Device("mlx5_0/1"), Title("port 1")); err != nil {
		t.Fatalf("first register: %v", err)
	}
	// Same name, different device — must succeed (per task spec).
	if _, err := r.Register(TypeFloat, "rate_gbps",
		Device("mlx5_0/2"), Title("port 2")); err != nil {
		t.Fatalf("second register (different device): %v", err)
	}
	if r.Len() != 2 {
		t.Errorf("Len = %d, want 2", r.Len())
	}
}

func TestMetricRegistry_DuplicateSameNameSameDevice(t *testing.T) {
	r := NewMetricRegistry()

	if _, err := r.Register(TypeFloat, "x",
		Device("d"), Title("t")); err != nil {
		t.Fatalf("first register: %v", err)
	}
	_, err := r.Register(TypeFloat, "x", Device("d"), Title("t"))
	if !errors.Is(err, ErrMetricDuplicate) {
		t.Fatalf("second register with same (name, device): want ErrMetricDuplicate, got %v", err)
	}
}

func TestMetricRegistry_MissingTitle(t *testing.T) {
	r := NewMetricRegistry()
	_, err := r.Register(TypeFloat, "x")
	if !errors.Is(err, ErrMetricMissingTitle) {
		t.Fatalf("missing Title: want ErrMetricMissingTitle, got %v", err)
	}
}

func TestMetricRegistry_InvalidType(t *testing.T) {
	r := NewMetricRegistry()
	_, err := r.Register(MetricType("uint128"), "x", Title("t"))
	if !errors.Is(err, ErrMetricInvalidType) {
		t.Fatalf("bogus type: want ErrMetricInvalidType, got %v", err)
	}
}

func TestMetricRegistry_EmptyName(t *testing.T) {
	r := NewMetricRegistry()
	_, err := r.Register(TypeFloat, "", Title("t"))
	if !errors.Is(err, ErrMetricMissingName) {
		t.Fatalf("empty name: want ErrMetricMissingName, got %v", err)
	}
}

func TestMetricRegistry_AllSorted(t *testing.T) {
	r := NewMetricRegistry()
	r.MustRegister(TypeFloat, "b", Title("b"))
	r.MustRegister(TypeFloat, "a", Device("d2"), Title("a/d2"))
	r.MustRegister(TypeFloat, "a", Device("d1"), Title("a/d1"))

	all := r.All()
	if len(all) != 3 {
		t.Fatalf("All len = %d, want 3", len(all))
	}
	// Expected order: (a, d1), (a, d2), (b, "")
	want := [][2]string{{"a", "d1"}, {"a", "d2"}, {"b", ""}}
	for i, w := range want {
		if all[i].Name != w[0] || all[i].Device != w[1] {
			t.Errorf("All()[%d] = (%s, %s), want (%s, %s)",
				i, all[i].Name, all[i].Device, w[0], w[1])
		}
	}
}

func TestMetricRegistry_SampleEmitsForeignKey(t *testing.T) {
	r := NewMetricRegistry()
	r.MustRegister(TypeFloat, "cpu_temp",
		Device("cpu0"), Unit("celsius"), Title("CPU 0 temperature"))

	s := r.Sample("cpu_temp", "cpu0", 58.0)
	if s.MetricName != "cpu_temp" {
		t.Errorf("Sample.MetricName = %q, want cpu_temp", s.MetricName)
	}
	if s.Unit != "celsius" {
		t.Errorf("Sample.Unit = %q, want celsius", s.Unit)
	}
	if s.Labels["device"] != "cpu0" {
		t.Errorf("Sample.Labels[device] = %q, want cpu0", s.Labels["device"])
	}
}

func TestMetricRegistry_SampleUnknownPanics(t *testing.T) {
	r := NewMetricRegistry()
	defer func() {
		if rec := recover(); rec == nil {
			t.Errorf("Sample on unknown metric: expected panic")
		}
	}()
	_ = r.Sample("nope", "", 1.0)
}

func TestMetricRegistry_MustRegisterPanicsOnDuplicate(t *testing.T) {
	r := NewMetricRegistry()
	r.MustRegister(TypeFloat, "x", Title("t"))
	defer func() {
		if rec := recover(); rec == nil {
			t.Errorf("MustRegister duplicate: expected panic")
		}
	}()
	r.MustRegister(TypeFloat, "x", Title("t"))
}
