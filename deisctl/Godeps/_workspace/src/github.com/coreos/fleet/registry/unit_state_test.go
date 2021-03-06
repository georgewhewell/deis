package registry

import (
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/coreos/fleet/etcd"
	"github.com/coreos/fleet/machine"
	"github.com/coreos/fleet/unit"
)

type action struct {
	key string
	val string
	rec bool
}

type testEtcdClient struct {
	gets    []action
	sets    []action
	deletes []action
	res     []*etcd.Result // errors returned from subsequent calls to etcd
	ri      int
	err     []error // results returned from subsequent calls to etcd
	ei      int
}

func (t *testEtcdClient) Do(req etcd.Action) (r *etcd.Result, e error) {
	if s, ok := req.(*etcd.Set); ok {
		t.sets = append(t.sets, action{key: s.Key, val: s.Value})
	} else if d, ok := req.(*etcd.Delete); ok {
		t.deletes = append(t.deletes, action{key: d.Key, rec: d.Recursive})
	} else if g, ok := req.(*etcd.Get); ok {
		t.gets = append(t.gets, action{key: g.Key, rec: g.Recursive})
	}
	if t.ri < len(t.res) {
		r = t.res[t.ri]
		t.ri++
	}
	if t.ei < len(t.err) {
		e = t.err[t.ei]
		t.ei++
	}
	return r, e
}

func (t *testEtcdClient) Wait(req etcd.Action, ch <-chan struct{}) (*etcd.Result, error) {
	return t.Do(req)
}

func TestUnitStatePaths(t *testing.T) {
	r := &EtcdRegistry{nil, "/fleet/"}
	j := "foo.service"
	want := "/fleet/state/foo.service"
	got := r.legacyUnitStatePath(j)
	if got != want {
		t.Errorf("bad unit state path: got %v, want %v", got, want)
	}
	m := "abcdefghij"
	want = "/fleet/states/foo.service/abcdefghij"
	got = r.unitStatePath(m, j)
	if got != want {
		t.Errorf("bad unit state path: got %v, want %v", got, want)
	}
}

func TestSaveUnitState(t *testing.T) {
	e := &testEtcdClient{}
	r := &EtcdRegistry{e, "/fleet/"}
	j := "foo.service"
	mID := "mymachine"
	us := unit.NewUnitState("abc", "def", "ghi", mID)

	// Saving nil unit state should fail
	r.SaveUnitState(j, nil, time.Second)
	if e.sets != nil || e.deletes != nil {
		t.Logf("sets: %#v", e.sets)
		t.Logf("deletes: %#v", e.deletes)
		t.Fatalf("SaveUnitState of nil state should fail but acted unexpectedly!")
	}

	// Saving unit state with no hash should succeed for now, but should fail
	// in the future. See https://github.com/coreos/fleet/issues/720.
	//r.SaveUnitState(j, us, time.Second)
	//if len(e.sets) != 1 || e.deletes == nil {
	//	t.Logf("sets: %#v", e.sets)
	//	t.Logf("deletes: %#v", e.deletes)
	//	t.Fatalf("SaveUnitState on UnitState with no hash acted unexpectedly!")
	//}

	us.UnitHash = "quickbrownfox"
	r.SaveUnitState(j, us, time.Second)

	json := `{"loadState":"abc","activeState":"def","subState":"ghi","machineState":{"ID":"mymachine","PublicIP":"","Metadata":null,"Version":""},"unitHash":"quickbrownfox"}`
	p1 := "/fleet/state/foo.service"
	p2 := "/fleet/states/foo.service/mymachine"
	want := []action{
		action{key: p1, val: json},
		action{key: p2, val: json},
	}
	got := e.sets
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bad result from SaveUnitState: \ngot\n%#v\nwant\n%#v", got, want)
	}
	if e.deletes != nil {
		t.Errorf("unexpected deletes during SaveUnitState: %#v", e.deletes)
	}
	if e.gets != nil {
		t.Errorf("unexpected gets during SaveUnitState: %#v", e.gets)
	}
}

func TestRemoveUnitState(t *testing.T) {
	e := &testEtcdClient{}
	r := &EtcdRegistry{e, "/fleet/"}
	j := "foo.service"
	err := r.RemoveUnitState(j)
	if err != nil {
		t.Errorf("unexpected error from RemoveUnitState: %v", err)
	}
	want := []action{
		action{key: "/fleet/state/foo.service", rec: false},
		action{key: "/fleet/states/foo.service", rec: true},
	}
	got := e.deletes
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bad result from RemoveUnitState: \ngot\n%#v\nwant\n%#v", got, want)
	}
	if e.sets != nil {
		t.Errorf("unexpected sets during RemoveUnitState: %#v", e.sets)
	}
	if e.gets != nil {
		t.Errorf("unexpected gets during RemoveUnitState: %#v", e.gets)
	}

	// Ensure RemoveUnitState handles different error scenarios appropriately
	for i, tt := range []struct {
		errs []error
		fail bool
	}{
		{[]error{etcd.Error{ErrorCode: etcd.ErrorKeyNotFound}}, false},
		{[]error{nil, etcd.Error{ErrorCode: etcd.ErrorKeyNotFound}}, false},
		{[]error{nil, nil}, false}, // No errors, no responses should succeed
		{[]error{errors.New("ur registry don't work")}, true},
		{[]error{nil, errors.New("ur registry don't work")}, true},
	} {
		e = &testEtcdClient{err: tt.errs}
		r = &EtcdRegistry{e, "/fleet"}
		err = r.RemoveUnitState("foo.service")
		if (err != nil) != tt.fail {
			t.Errorf("case %d: unexpected error state calling UnitStates(): got %v, want %v", i, err, tt.fail)
		}
	}
}

func TestUnitStateToModel(t *testing.T) {
	for i, tt := range []struct {
		in   *unit.UnitState
		want *unitStateModel
	}{
		{
			in:   nil,
			want: nil,
		},
		{
			// Unit state with no hash and no machineID is OK
			// See https://github.com/coreos/fleet/issues/720
			in:   &unit.UnitState{"foo", "bar", "baz", "", "", "name"},
			want: &unitStateModel{"foo", "bar", "baz", nil, ""},
		},
		{
			// Unit state with hash but no machineID is OK
			in:   &unit.UnitState{"foo", "bar", "baz", "", "heh", "name"},
			want: &unitStateModel{"foo", "bar", "baz", nil, "heh"},
		},
		{
			in:   &unit.UnitState{"foo", "bar", "baz", "woof", "miaow", "name"},
			want: &unitStateModel{"foo", "bar", "baz", &machine.MachineState{ID: "woof"}, "miaow"},
		},
	} {
		got := unitStateToModel(tt.in)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("case %d: got %#v, want %#v", i, got, tt.want)
		}
	}
}

func TestModelToUnitState(t *testing.T) {
	for i, tt := range []struct {
		in   *unitStateModel
		want *unit.UnitState
	}{
		{
			in:   nil,
			want: nil,
		},
		{
			in: &unitStateModel{"foo", "bar", "baz", nil, ""},
			want: &unit.UnitState{
				LoadState:   "foo",
				ActiveState: "bar",
				SubState:    "baz",
				MachineID:   "",
				UnitHash:    "",
				UnitName:    "name",
			},
		},
		{
			in: &unitStateModel{"z", "x", "y", &machine.MachineState{ID: "abcd"}, ""},
			want: &unit.UnitState{
				LoadState:   "z",
				ActiveState: "x",
				SubState:    "y",
				MachineID:   "abcd",
				UnitHash:    "",
				UnitName:    "name",
			},
		},
	} {
		got := modelToUnitState(tt.in, "name")
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("case %d: got %#v, want %#v", i, got, tt.want)
		}
	}
}

func makeResult(val string) *etcd.Result {
	return &etcd.Result{
		Node: &etcd.Node{
			Value: val,
		},
	}
}

func TestGetUnitState(t *testing.T) {
	for i, tt := range []struct {
		res *etcd.Result // result returned from etcd
		err error        // error returned from etcd
		us  *unit.UnitState
	}{
		{
			// Unit state with no UnitHash should be OK
			res: makeResult(`{"loadState":"abc","activeState":"def","subState":"ghi","machineState":{"ID":"mymachine","PublicIP":"","Metadata":null,"Version":"","TotalResources":{"Cores":0,"Memory":0,"Disk":0},"FreeResources":{"Cores":0,"Memory":0,"Disk":0}}}`),
			err: nil,
			us:  &unit.UnitState{"abc", "def", "ghi", "mymachine", "", "foo.service"},
		},
		{
			// Unit state with UnitHash should be OK
			res: makeResult(`{"loadState":"abc","activeState":"def","subState":"ghi","machineState":{"ID":"mymachine","PublicIP":"","Metadata":null,"Version":"","TotalResources":{"Cores":0,"Memory":0,"Disk":0},"FreeResources":{"Cores":0,"Memory":0,"Disk":0}},"unitHash":"quickbrownfox"}`),
			err: nil,
			us:  &unit.UnitState{"abc", "def", "ghi", "mymachine", "quickbrownfox", "foo.service"},
		},
		{
			// Unit state with no MachineState should be OK
			res: makeResult(`{"loadState":"abc","activeState":"def","subState":"ghi"}`),
			err: nil,
			us:  &unit.UnitState{"abc", "def", "ghi", "", "", "foo.service"},
		},
		{
			// Bad unit state object should simply result in nil returned
			res: makeResult(`garbage, not good proper json`),
			err: nil,
			us:  nil,
		},
		{
			// Unknown errors should result in nil returned
			res: nil,
			err: errors.New("some random error from etcd"),
			us:  nil,
		},
		{
			// KeyNotFound should result in nil returned
			res: nil,
			err: etcd.Error{ErrorCode: etcd.ErrorKeyNotFound},
			us:  nil,
		},
	} {
		e := &testEtcdClient{
			res: []*etcd.Result{tt.res},
			err: []error{tt.err},
		}
		r := &EtcdRegistry{e, "/fleet/"}
		j := "foo.service"
		us := r.getUnitState(j)
		want := []action{
			action{key: "/fleet/state/foo.service", rec: true},
		}
		got := e.gets
		if !reflect.DeepEqual(got, want) {
			t.Errorf("case %d: bad result from GetUnitState:\ngot\n%#v\nwant\n%#v", i, got, want)
		}
		if !reflect.DeepEqual(us, tt.us) {
			t.Errorf("case %d: bad UnitState:\ngot\n%#v\nwant\n%#v", i, us, tt.us)
		}
	}
}

func usToJson(t *testing.T, us *unit.UnitState) string {
	json, err := marshal(unitStateToModel(us))
	if err != nil {
		t.Fatalf("error marshalling unit: %v", err)
	}
	return json
}

func TestUnitStates(t *testing.T) {
	fus1 := unit.UnitState{"abc", "def", "ghi", "mID1", "zzz", "foo"}
	fus2 := unit.UnitState{"cat", "dog", "cow", "mID2", "xxx", "foo"}
	// Multiple new unit states reported for the same unit
	foo := etcd.Node{
		Key: "/fleet/states/foo",
		Nodes: []etcd.Node{
			etcd.Node{
				Key:   "/fleet/states/foo/mID1",
				Value: usToJson(t, &fus1),
			},
			etcd.Node{
				Key:   "/fleet/states/foo/mID2",
				Value: usToJson(t, &fus2),
			},
		},
	}
	// Bogus new unit state which we won't expect to see in results
	bar := etcd.Node{
		Key: "/fleet/states/bar",
		Nodes: []etcd.Node{
			etcd.Node{
				Key:   "/fleet/states/bar/asdf",
				Value: `total garbage`,
			},
		},
	}
	// Legacy unit state which we expect to be overridden by fus1 (from the
	// same machine ID)
	fus3 := unit.UnitState{"cba", "fed", "ihg", "mID1", "zzz", "foo"}
	bfoo := etcd.Node{
		Key:   "/fleet/state/foo",
		Value: usToJson(t, &fus3),
	}
	// Legacy unit state which we expect to see in the results
	bus := unit.UnitState{"111", "222", "333", "mID3", "aaa", "bar"}
	baz := etcd.Node{
		Key:   "/fleet/state/bar",
		Value: usToJson(t, &bus),
	}

	// Result from crawling the legacy "state" namespace
	res1 := &etcd.Result{
		Node: &etcd.Node{
			Key:   "/fleet/state",
			Nodes: []etcd.Node{bfoo, baz},
		},
	}
	// Result from crawling the new "states" namespace
	res2 := &etcd.Result{
		Node: &etcd.Node{
			Key:   "/fleet/states",
			Nodes: []etcd.Node{foo, bar},
		},
	}
	e := &testEtcdClient{
		res: []*etcd.Result{res1, res2},
	}
	r := &EtcdRegistry{e, "/fleet/"}

	got, err := r.UnitStates()
	if err != nil {
		t.Errorf("unexpected error calling UnitStates(): %v", err)
	}

	want := []*unit.UnitState{
		&bus,
		&fus1,
		&fus2,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UnitStates() returned unexpected result")
		t.Log("got:")
		for _, i := range got {
			t.Logf("%#v", i)
		}
		t.Log("want:")
		for _, i := range want {
			t.Logf("%#v", i)
		}
	}

	// Ensure UnitState handles different error scenarios appropriately
	for i, tt := range []struct {
		errs []error
		fail bool
	}{
		{[]error{etcd.Error{ErrorCode: etcd.ErrorKeyNotFound}}, false},
		{[]error{nil, etcd.Error{ErrorCode: etcd.ErrorKeyNotFound}}, false},
		{[]error{nil, nil}, false}, // No errors, no responses should succeed
		{[]error{errors.New("ur registry don't work")}, true},
		{[]error{nil, errors.New("ur registry don't work")}, true},
	} {
		e = &testEtcdClient{err: tt.errs}
		r = &EtcdRegistry{e, "/fleet"}
		got, err = r.UnitStates()
		if (err != nil) != tt.fail {
			t.Errorf("case %d: unexpected error state calling UnitStates(): got %v, want %v", i, err, tt.fail)
		}
		if len(got) != 0 {
			t.Errorf("case %d: UnitStates() returned unexpected non-empty result on error: %v", i, got)
		}
	}
}

func TestMUSKeys(t *testing.T) {
	equal := func(a MUSKeys, b []MUSKey) bool {
		if len(a) != len(b) {
			return false
		}
		for i, m := range a {
			if m != b[i] {
				return false
			}
		}
		return true
	}
	k1 := MUSKey{name: "abc", machID: "aaa"}
	k2 := MUSKey{name: "abc", machID: "zzz"}
	k3 := MUSKey{name: "def", machID: "bbb"}
	k4 := MUSKey{name: "ppp", machID: "zzz"}
	k5 := MUSKey{name: "xxx", machID: "aaa"}
	want := []MUSKey{k1, k2, k3, k4, k5}
	ms := MUSKeys{k3, k4, k5, k2, k1}
	if equal(ms, want) {
		t.Fatalf("this should never happen!")
	}
	sort.Sort(ms)
	if !equal(ms, want) {
		t.Errorf("bad result after sort: got\n%#v, want\n%#v", ms, want)
	}
}
