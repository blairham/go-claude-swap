// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package autoswitch

import "testing"

func TestSwitchOriginCarriesTriggerAndUtilization(t *testing.T) {
	e := &Engine{Config: Config{Threshold: 90}}
	h := 6.5
	o := e.switchOrigin(triggerProactive, &h)
	if o.Trigger != triggerProactive || o.Source != "auto" || o.ActiveUtilizationPct == nil ||
		*o.ActiveUtilizationPct != 93.5 || o.Reason != "active at 93.5% (threshold 90%)" {
		t.Errorf("proactive origin = %+v", o)
	}
	zero := 0.0
	if o := e.switchOrigin(
		triggerAtLimit,
		&zero,
	); *o.ActiveUtilizationPct != 100 ||
		o.Reason != "active account at its limit" {
		t.Errorf("at-limit origin = %+v", o)
	}
	if o := e.switchOrigin(
		triggerFailover,
		nil,
	); o.ActiveUtilizationPct != nil || o.Trigger != triggerFailover ||
		o.Reason == "" {
		t.Errorf("failover origin = %+v", o)
	}
}
