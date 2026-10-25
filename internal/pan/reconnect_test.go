package pan

import (
	"context"
	"testing"
)

func TestInactivePANActivatesOnlyValidatedProfile(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string{}, args...))
		switch len(calls) {
		case 1:
			return "unrelated-profile\n", nil
		case 2:
			return "02:00:00:00:00:01\npanu\n", nil
		default:
			return "", nil
		}
	}
	id := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	if err := Ensure(context.Background(), run, id, "02:00:00:00:00:01"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("wanted validation plus activation, got %v", calls)
	}
	want := []string{"--wait", "10", "connection", "up", "uuid", id}
	if len(calls[2]) != len(want) {
		t.Fatal(calls[2])
	}
	for i, s := range want {
		if calls[2][i] != s {
			t.Fatal(calls[2])
		}
	}
}

func TestInvalidPANIdentityNeverRunsCommands(t *testing.T) {
	for _, input := range [][2]string{{"", "02:00:00:00:00:01"}, {"../../etc/passwd", "02:00:00:00:00:01"}, {"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "not-a-mac"}} {
		calls := 0
		run := func(_ context.Context, _ ...string) (string, error) { calls++; return "", nil }
		if err := Ensure(context.Background(), run, input[0], input[1]); err == nil {
			t.Errorf("accepted invalid identity %v", input)
		}
		if calls != 0 {
			t.Errorf("invalid identity ran %d commands", calls)
		}
	}
}

func TestUpperCaseUUIDAlreadyActiveIsReadOnly(t *testing.T) {
	calls := 0
	run := func(_ context.Context, args ...string) (string, error) {
		calls++
		return "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee\n", nil
	}
	err := Ensure(context.Background(), run, "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE", "02:00:00:00:00:01")
	if calls != 1 {
		t.Fatalf("active connection must require exactly one read call, got %d (err=%v)", calls, err)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestAlreadyActivePANIsNotBounced(t *testing.T) {
	calls := 0
	run := func(_ context.Context, args ...string) (string, error) {
		calls++
		return "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee\n", nil
	}
	if err := Ensure(context.Background(), run, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "02:00:00:00:00:01"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("active connection was touched: %d calls", calls)
	}
}
