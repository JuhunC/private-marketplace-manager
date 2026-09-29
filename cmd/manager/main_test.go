package main

import (
	"fmt"
	"testing"
)

func TestTrustedProxies(t *testing.T) {
	got, e := trustedProxies(" 172.18.0.1, 10.1.0.0/16 ,::1,::ffff:192.0.2.1,")
	if e != nil || fmt.Sprint(got) != "[172.18.0.1/32 10.1.0.0/16 ::1/128 192.0.2.1/32]" {
		t.Fatalf("%v %v", got, e)
	}
	if got, e := trustedProxies(""); e != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, e)
	}
	for _, bad := range []string{"bogus", "10.0.0.0/33", "300.1.1.1"} {
		if _, e := trustedProxies(bad); e == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}
