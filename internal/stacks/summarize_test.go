package stacks

import "testing"

func TestSummarize(t *testing.T) {
	ctr := func(state string) *Container { return &Container{State: state} }
	cases := []struct {
		name string
		ctrs [][]*Container // per service
		want State
		up   int
	}{
		{"all running", [][]*Container{{ctr("running")}, {ctr("running")}}, Running, 2},
		{"one stopped", [][]*Container{{ctr("running")}, {ctr("exited")}}, Partial, 1},
		{"all stopped", [][]*Container{{ctr("exited")}}, Stopped, 0},
		{"all paused", [][]*Container{{ctr("paused")}, {ctr("paused")}}, Paused, 0},
		{"paused and running", [][]*Container{{ctr("running")}, {ctr("paused")}}, Partial, 1},
		{"restarting", [][]*Container{{ctr("restarting")}}, Problem, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &Stack{}
			for i, cs := range c.ctrs {
				st.Services = append(st.Services, &Service{Name: string(rune('a' + i)), Containers: cs})
			}
			st.summarize()
			if st.State != c.want || st.Up != c.up {
				t.Errorf("state = %s, up = %d; want %s, %d", st.State, st.Up, c.want, c.up)
			}
		})
	}
}
