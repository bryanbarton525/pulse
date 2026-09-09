package proberunner

import (
	"time"

	"github.com/bryanbarton525/pulse/internal/observation"
)

func testObservation() observation.Observation {
	return observation.Observation{
		Probe: "default/probe",
		Kind:  observation.KindFailure,
		Text:  "type=http status=<num> message=boom",
		At:    time.Now(),
	}
}
