package logs

import "testing"

func TestMeasurementExitsAreNotEvents(t *testing.T) {
	var d Detector
	exit := Line{Message: "srv           run: instance name=a exited with status 0"}
	if got := d.Observe(exit); got != nil {
		t.Errorf("measurement exit produced %v", got)
	}
	d.Observe(Line{Message: "srv  load: spawning server instance with name=a on port 5"})
	got := d.Observe(exit)
	if len(got) != 1 || got[0].Title != "Unloaded a" {
		t.Errorf("unload produced %v", got)
	}
	if got := d.Observe(Line{Message: "srv           run: instance name=a exited with status 1"}); len(got) != 1 || got[0].Severity != SeverityError {
		t.Errorf("crash produced %v", got)
	}
}
