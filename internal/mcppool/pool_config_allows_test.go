package mcppool

import "testing"

func TestPoolConfigAllows(t *testing.T) {
	cases := []struct {
		cfg  *PoolConfig
		name string
		want bool
	}{
		{nil, "x", false},
		{&PoolConfig{Enabled: false, PoolAll: true}, "x", false},
		{&PoolConfig{Enabled: false, PoolMCPs: []string{"x"}}, "x", false},
		{&PoolConfig{Enabled: true, PoolMCPs: []string{"x"}}, "x", true},
		{&PoolConfig{Enabled: true, PoolMCPs: []string{"x"}}, "y", false},
		{&PoolConfig{Enabled: true}, "x", false},
		{&PoolConfig{Enabled: true, PoolAll: true, ExcludeMCPs: []string{"y"}}, "y", false},
		{&PoolConfig{Enabled: true, PoolAll: true, ExcludeMCPs: []string{"y"}}, "x", true},
	}
	for _, c := range cases {
		if got := c.cfg.Allows(c.name); got != c.want {
			t.Errorf("Allows(%+v, %q) = %v, want %v", c.cfg, c.name, got, c.want)
		}
		if c.cfg != nil {
			p := &Pool{config: c.cfg}
			if got := p.ShouldPool(c.name); got != c.want {
				t.Errorf("ShouldPool(%+v, %q) = %v, want %v", c.cfg, c.name, got, c.want)
			}
		}
	}
}
