package incident

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/bryanbarton525/pulse/internal/embed"
	"github.com/bryanbarton525/pulse/internal/observation"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// Dispatcher performs a policy's actions for an incident. It is an interface
// so the engine's correlation logic can be tested without any network.
type Dispatcher interface {
	Dispatch(ctx context.Context, incident *Incident)
}

// SelectorParser turns a serialized label selector into a predicate. Injected
// so this package stays free of Kubernetes dependencies.
type SelectorParser func(string) (Selector, error)

// Engine converts a stream of observations into incidents.
//
// It runs as a single replica. Probe runners shard by probe name and each
// embeds its own hot-path work locally, but correlation has to see the whole
// cluster at once: canaries that share a backend are hashed onto different
// shards, so a per-shard view would systematically miss exactly the incidents
// worth catching.
type Engine struct {
	mu      sync.Mutex
	open    map[string]*Incident
	byProbe map[string]string
	counter int

	window    *Window
	graph     *Graph
	novelty   *NoveltyIndex
	inference *Inference

	// probes is the flattened config the engine mounts from the same ConfigMap
	// the runners read, so it knows every probe's policy without any of that
	// travelling over the wire.
	probes map[string]proberunner.Probe

	embedder   embed.Embedder
	dispatcher Dispatcher
	parse      SelectorParser
	logger     logr.Logger

	// now is injectable so tests can drive time deterministically.
	now func() time.Time
}

// EngineOptions configures a new Engine.
type EngineOptions struct {
	Embedder   embed.Embedder
	Dispatcher Dispatcher
	Parse      SelectorParser
	Logger     logr.Logger
	Now        func() time.Time

	WindowCapacity int
	MaxClusters    int
}

// NewEngine builds an engine with no probes loaded yet.
func NewEngine(options EngineOptions) *Engine {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	parse := options.Parse
	if parse == nil {
		parse = func(string) (Selector, error) { return nil, nil }
	}

	return &Engine{
		open:       map[string]*Incident{},
		byProbe:    map[string]string{},
		window:     NewWindow(options.WindowCapacity),
		graph:      NewGraph(),
		novelty:    NewNoveltyIndex(now(), options.MaxClusters),
		inference:  NewInference(),
		probes:     map[string]proberunner.Probe{},
		embedder:   options.Embedder,
		dispatcher: options.Dispatcher,
		parse:      parse,
		logger:     options.Logger,
		now:        now,
	}
}

// SetEmbedder swaps the failure-path model.
//
// Needed because a policy edit can change the model while the engine is
// running. Without this the engine would keep using whatever was loaded at
// startup and silently ignore the new configuration.
//
// Existing novelty clusters are discarded on a swap: they hold vectors from the
// old embedding space, and comparing those against the new model's output is
// meaningless. The settling period covers the resulting burst of "new" shapes.
func (e *Engine) SetEmbedder(embedder embed.Embedder) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.embedder == embedder {
		return
	}

	e.embedder = embedder
	e.novelty = NewNoveltyIndex(e.now(), 0)
}

// LoadProbes replaces the engine's view of probe configuration and rebuilds the
// dependency graph. Called at startup and whenever the mounted ConfigMap changes.
func (e *Engine) LoadProbes(probes []proberunner.Probe) {
	indexed := make(map[string]proberunner.Probe, len(probes))
	for _, probe := range probes {
		indexed[probe.Name] = probe
	}

	graph := BuildGraph(probes)

	e.mu.Lock()
	defer e.mu.Unlock()

	e.probes = indexed
	e.graph = graph
}

// Ingest processes one observation from a probe runner.
func (e *Engine) Ingest(ctx context.Context, signal observation.Observation) {
	switch signal.Kind {
	case observation.KindRecovery:
		e.handleRecovery(signal)
	case observation.KindFailure:
		e.handleFailure(ctx, signal)
	case observation.KindBodyDrift, observation.KindLatencyShift:
		e.handleSingleProbeSignal(ctx, signal)
	}
}

// handleRecovery removes a probe from the correlation window and from any
// incident it belongs to, closing the incident once everyone has recovered.
func (e *Engine) handleRecovery(signal observation.Observation) {
	e.window.Remove(signal.Probe)

	e.mu.Lock()
	defer e.mu.Unlock()

	incidentID, found := e.byProbe[signal.Probe]
	if !found {
		return
	}
	delete(e.byProbe, signal.Probe)

	current, found := e.open[incidentID]
	if !found {
		return
	}

	if !current.remove(signal.Probe) {
		delete(e.open, incidentID)
		return
	}

	// The root cause recovering does not end the incident — the victims may
	// still be down. Re-rank among whoever is left.
	if current.RootCause == signal.Probe {
		e.rerankLocked(current)
	}
	current.UpdatedAt = signal.At
}

// handleFailure is the correlation path.
func (e *Engine) handleFailure(ctx context.Context, signal observation.Observation) {
	probe, settings, ok := e.correlationSettings(signal.Probe)
	if !ok {
		return
	}

	vector := e.embed(ctx, signal.Text)

	candidate := Candidate{
		Probe:  signal.Probe,
		Vector: vector,
		Labels: signal.Labels,
		At:     signal.At,
	}

	window := time.Duration(settings.WindowSeconds) * time.Second

	// Learn co-occurrence from onsets. This only ever produces proposals; it
	// never feeds back into merging.
	if probe.Intelligence.Topology.InferDependencies {
		e.inference.RecordOnset(signal.Probe, signal.At, window)
	}

	selector := e.selector(settings.CandidateSelector)
	related := e.relatedFailures(candidate, settings, selector, window)

	e.window.Add(candidate)

	e.mu.Lock()
	defer e.mu.Unlock()

	current := e.incidentForLocked(signal.Probe, related, signal.At)
	e.attachLocked(current, signal, vector)
	e.rerankLocked(current)

	// Novelty is judged on the ROOT CAUSE's failure, not on whichever victim
	// happened to report last, so the same outage classifies the same way
	// regardless of which canary noticed first.
	e.classifyNoveltyLocked(current, probe)

	current.UpdatedAt = signal.At
	e.dispatchLocked(ctx, current)
}

// handleSingleProbeSignal covers drift and latency, which concern one probe by
// construction: a body that changed meaning is a statement about that endpoint
// alone, and there is nothing to correlate it with.
func (e *Engine) handleSingleProbeSignal(ctx context.Context, signal observation.Observation) {
	probe, found := e.probe(signal.Probe)
	if !found || probe.Intelligence == nil {
		return
	}

	trigger := TriggerBodyDrift
	if signal.Kind == observation.KindLatencyShift {
		trigger = TriggerLatencyShift
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.counter++
	current := &Incident{
		ID:        fmt.Sprintf("inc-%d-%d", signal.At.Unix(), e.counter),
		Trigger:   trigger,
		RootCause: signal.Probe,
		Policy:    probe.Intelligence.Policy,
		OpenedAt:  signal.At,
		UpdatedAt: signal.At,
		Members: []Member{{
			Probe:  signal.Probe,
			Role:   RoleRootCause,
			Signal: signal,
		}},
	}
	current.Signature = computeSignature(trigger, signal.Probe, "", []string{signal.Probe})

	e.dispatchLocked(ctx, current)
}

// relatedFailures finds recent failures that share evidence with this one.
func (e *Engine) relatedFailures(
	candidate Candidate,
	settings proberunner.ProbeFailureCorrelationTrigger,
	selector Selector,
	window time.Duration,
) []Candidate {
	e.mu.Lock()
	graph := e.graph
	e.mu.Unlock()

	recent := e.window.Recent(candidate.At, window, candidate.Probe)

	var related []Candidate
	for _, other := range recent {
		// The selector is a guardrail for hard boundaries — never correlate
		// dev with prod — applied to both sides.
		if !MatchesSelector(selector, candidate.Labels) || !MatchesSelector(selector, other.Labels) {
			continue
		}
		if Evaluate(candidate, other, graph, settings.SimilarityThreshold).Merge {
			related = append(related, other)
		}
	}

	return related
}

// incidentForLocked finds or creates the incident this failure belongs to.
func (e *Engine) incidentForLocked(probe string, related []Candidate, at time.Time) *Incident {
	// Already part of an open incident.
	if incidentID, found := e.byProbe[probe]; found {
		if current, found := e.open[incidentID]; found {
			return current
		}
	}

	// Join the incident of anything it correlates with. Earliest first, so a
	// late arrival joins the incident that started first rather than splitting
	// the outage in two.
	for _, other := range related {
		if incidentID, found := e.byProbe[other.Probe]; found {
			if current, found := e.open[incidentID]; found {
				return current
			}
		}
	}

	e.counter++
	current := &Incident{
		ID:        fmt.Sprintf("inc-%d-%d", at.Unix(), e.counter),
		Trigger:   TriggerFailureCorrelation,
		OpenedAt:  at,
		UpdatedAt: at,
	}
	e.open[current.ID] = current

	// Pull in the correlated failures that were not already in an incident.
	for _, other := range related {
		if _, found := e.byProbe[other.Probe]; found {
			continue
		}
		e.addMemberLocked(current, Member{
			Probe:  other.Probe,
			Signal: observation.Observation{Probe: other.Probe, Kind: observation.KindFailure, At: other.At},
		})
	}

	return current
}

func (e *Engine) attachLocked(current *Incident, signal observation.Observation, _ embed.Vector) {
	for index := range current.Members {
		if current.Members[index].Probe == signal.Probe {
			current.Members[index].Signal = signal
			e.byProbe[signal.Probe] = current.ID
			return
		}
	}

	e.addMemberLocked(current, Member{Probe: signal.Probe, Signal: signal})
}

func (e *Engine) addMemberLocked(current *Incident, member Member) {
	current.Members = append(current.Members, member)
	e.byProbe[member.Probe] = current.ID
	e.open[current.ID] = current
}

// rerankLocked recomputes who is to blame and relabels every member.
func (e *Engine) rerankLocked(current *Incident) {
	if len(current.Members) == 0 {
		return
	}

	// Candidates must be ordered by onset: root-cause ranking breaks ties by
	// whatever broke first.
	ordered := append([]Member(nil), current.Members...)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j].Signal.At.Before(ordered[j-1].Signal.At); j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}

	failing := make([]string, 0, len(ordered))
	for _, member := range ordered {
		failing = append(failing, member.Probe)
	}

	current.RootCause = e.graph.RankRootCause(failing)
	roles := e.graph.Roles(failing, current.RootCause)
	for index := range current.Members {
		current.Members[index].Role = roles[current.Members[index].Probe]
	}

	// The root cause's policy owns the incident: one investigation, one
	// notification, sent to whoever owns the thing that actually broke.
	if probe, found := e.probes[current.RootCause]; found && probe.Intelligence != nil {
		current.Policy = probe.Intelligence.Policy
	}
}

func (e *Engine) classifyNoveltyLocked(current *Incident, probe proberunner.Probe) {
	novelty := probe.Intelligence.Triggers.FailureNovelty
	clusterID := ""

	if novelty != nil {
		root := current.RootCauseSignal()
		vector := e.embed(context.Background(), root.Text)
		if len(vector.Values) > 0 {
			result := e.novelty.Classify(
				vector,
				current.UpdatedAt,
				novelty.ClusterThreshold,
				time.Duration(novelty.SettlingPeriodSeconds)*time.Second,
			)
			clusterID = result.ClusterID
			current.Novel = result.Novel
		}
	}

	current.Signature = computeSignature(
		current.Trigger, current.RootCause, clusterID, current.ProbeNames())
}

func (e *Engine) dispatchLocked(ctx context.Context, current *Incident) {
	if e.dispatcher == nil {
		return
	}

	snapshot := *current
	snapshot.Members = append([]Member(nil), current.Members...)
	go e.dispatcher.Dispatch(ctx, &snapshot)
}

// correlationSettings returns a probe's flattened correlation configuration.
func (e *Engine) correlationSettings(
	name string,
) (proberunner.Probe, proberunner.ProbeFailureCorrelationTrigger, bool) {
	probe, found := e.probe(name)
	if !found || probe.Intelligence == nil {
		return probe, proberunner.ProbeFailureCorrelationTrigger{}, false
	}

	settings := probe.Intelligence.Triggers.FailureCorrelation
	if settings == nil {
		// Correlation is off, but novelty may still be on: treat the failure
		// as its own single-member incident.
		return probe, proberunner.ProbeFailureCorrelationTrigger{
			WindowSeconds:       1,
			SimilarityThreshold: 2, // unreachable, so nothing ever merges
		}, true
	}

	return probe, *settings, true
}

func (e *Engine) probe(name string) (proberunner.Probe, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	probe, found := e.probes[name]
	return probe, found
}

func (e *Engine) selector(serialized string) Selector {
	if serialized == "" {
		return nil
	}

	selector, err := e.parse(serialized)
	if err != nil {
		e.logger.Error(err, "Ignoring an unparseable correlation selector", "selector", serialized)
		return nil
	}
	return selector
}

// embed turns text into a cold-path vector, tolerating an absent or failing
// embedder: correlation then falls back to declared topology alone, which is
// worse but still correct.
func (e *Engine) embed(ctx context.Context, text string) embed.Vector {
	if e.embedder == nil || text == "" {
		return embed.Vector{}
	}

	vectors, err := e.embedder.Embed(ctx, []string{text})
	if err != nil || len(vectors) == 0 {
		if err != nil {
			e.logger.Error(err, "Embedding failed; falling back to declared topology only")
		}
		return embed.Vector{}
	}

	return vectors[0]
}

// Open returns a snapshot of the currently open incidents.
func (e *Engine) Open() []Incident {
	e.mu.Lock()
	defer e.mu.Unlock()

	incidents := make([]Incident, 0, len(e.open))
	for _, current := range e.open {
		snapshot := *current
		snapshot.Members = append([]Member(nil), current.Members...)
		incidents = append(incidents, snapshot)
	}

	return incidents
}

// Proposals exposes learned dependency edges for review.
func (e *Engine) Proposals() []Proposal {
	e.mu.Lock()
	probes := make([]proberunner.Probe, 0, len(e.probes))
	for _, probe := range e.probes {
		probes = append(probes, probe)
	}
	e.mu.Unlock()

	// Use the strictest thresholds across policies, so a permissive policy
	// cannot flood every other team's status with weak proposals.
	minObservations := 0
	minConfidence := 0.0
	for _, probe := range probes {
		if probe.Intelligence == nil || !probe.Intelligence.Topology.InferDependencies {
			continue
		}
		if probe.Intelligence.Topology.InferMinObservations > minObservations {
			minObservations = probe.Intelligence.Topology.InferMinObservations
		}
		if probe.Intelligence.Topology.InferMinConfidence > minConfidence {
			minConfidence = probe.Intelligence.Topology.InferMinConfidence
		}
	}

	if minObservations == 0 {
		return nil
	}

	return e.inference.Proposals(minObservations, minConfidence)
}

// DeclaredEdges exposes the operator-declared dependency graph.
func (e *Engine) DeclaredEdges() [][2]string {
	e.mu.Lock()
	graph := e.graph
	e.mu.Unlock()

	return graph.Edges()
}
