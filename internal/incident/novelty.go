package incident

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/bryanbarton525/pulse/internal/embed"
)

// NoveltyResult describes where a failure fell among known failure shapes.
type NoveltyResult struct {
	// ClusterID names the failure shape. It is stable across occurrences, so
	// it doubles as the deduplication key for throttling.
	ClusterID string

	// Novel reports a shape never seen before.
	Novel bool

	// Occurrences counts how many times this shape has been seen, including
	// this one.
	Occurrences int

	// Settling reports that the engine started too recently to trust novelty.
	Settling bool
}

// failureCluster is one learned failure shape.
type failureCluster struct {
	id          string
	centroid    embed.Vector
	occurrences int
	firstSeen   time.Time
	lastSeen    time.Time
}

// NoveltyIndex answers "have we seen this failure before?".
//
// This is a ROUTING function, not detection. Nothing here claims a failure is
// anomalous — a canary's failure is deterministic and expected. What it decides
// is where a failure deserves attention: a shape nobody has seen justifies
// waking a language model, while the four hundredth repeat of a known one
// justifies incrementing a counter and staying quiet.
//
// State is in memory only, which is why the settling period exists: right after
// a restart every shape looks new, and escalating all of them at once would be
// worse than staying silent for a few minutes.
type NoveltyIndex struct {
	mu          sync.Mutex
	clusters    []*failureCluster
	startedAt   time.Time
	maxClusters int
	sequence    int
}

// NewNoveltyIndex builds an index that remembers at most maxClusters shapes.
func NewNoveltyIndex(startedAt time.Time, maxClusters int) *NoveltyIndex {
	if maxClusters <= 0 {
		maxClusters = 512
	}
	return &NoveltyIndex{startedAt: startedAt, maxClusters: maxClusters}
}

// Classify places a failure vector into a cluster, creating one if nothing is
// close enough.
func (n *NoveltyIndex) Classify(
	vector embed.Vector,
	now time.Time,
	threshold float64,
	settling time.Duration,
) NoveltyResult {
	if len(vector.Values) == 0 {
		return NoveltyResult{}
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	settlingNow := now.Before(n.startedAt.Add(settling))

	// Greedy nearest match. A linear scan is right here: the cluster count is
	// bounded in the hundreds and this runs only on failures.
	var best *failureCluster
	bestSimilarity := threshold
	for _, cluster := range n.clusters {
		if cluster.centroid.Space != vector.Space {
			continue
		}
		similarity := embed.Cosine(cluster.centroid, vector)
		if similarity >= bestSimilarity {
			best = cluster
			bestSimilarity = similarity
		}
	}

	if best != nil {
		best.occurrences++
		best.lastSeen = now
		best.absorb(vector)
		return NoveltyResult{
			ClusterID:   best.id,
			Novel:       false,
			Occurrences: best.occurrences,
			Settling:    settlingNow,
		}
	}

	n.sequence++
	cluster := &failureCluster{
		id:          newClusterID(vector, n.sequence),
		centroid:    embed.Vector{Space: vector.Space, Values: append([]float32(nil), vector.Values...)},
		occurrences: 1,
		firstSeen:   now,
		lastSeen:    now,
	}
	n.clusters = append(n.clusters, cluster)
	n.evictLocked()

	return NoveltyResult{
		ClusterID: cluster.id,
		// During settling the shape is recorded but not treated as news.
		Novel:       !settlingNow,
		Occurrences: 1,
		Settling:    settlingNow,
	}
}

// absorb nudges the centroid toward a new member, so a cluster tracks the
// family of failures it represents rather than only its first example.
func (c *failureCluster) absorb(vector embed.Vector) {
	alpha := 1 / float64(c.occurrences)
	if alpha < 0.05 {
		alpha = 0.05
	}

	for index := range c.centroid.Values {
		blended := float64(c.centroid.Values[index])*(1-alpha) + float64(vector.Values[index])*alpha
		c.centroid.Values[index] = float32(blended)
	}
	normalizeVector(c.centroid.Values)
}

// evictLocked drops the least useful cluster when the index is full, preferring
// to forget rare shapes that have not recurred.
func (n *NoveltyIndex) evictLocked() {
	if len(n.clusters) <= n.maxClusters {
		return
	}

	worst := 0
	for index := 1; index < len(n.clusters); index++ {
		candidate := n.clusters[index]
		incumbent := n.clusters[worst]
		if candidate.occurrences < incumbent.occurrences ||
			(candidate.occurrences == incumbent.occurrences && candidate.lastSeen.Before(incumbent.lastSeen)) {
			worst = index
		}
	}

	n.clusters = append(n.clusters[:worst], n.clusters[worst+1:]...)
}

// Size reports how many distinct failure shapes are remembered.
func (n *NoveltyIndex) Size() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.clusters)
}

// ClusterSummary is a read-only view of a learned failure shape.
type ClusterSummary struct {
	ID          string    `json:"id"`
	Occurrences int       `json:"occurrences"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
}

// Summaries lists known shapes, most frequent first.
func (n *NoveltyIndex) Summaries() []ClusterSummary {
	n.mu.Lock()
	defer n.mu.Unlock()

	summaries := make([]ClusterSummary, 0, len(n.clusters))
	for _, cluster := range n.clusters {
		summaries = append(summaries, ClusterSummary{
			ID:          cluster.id,
			Occurrences: cluster.occurrences,
			FirstSeen:   cluster.firstSeen,
			LastSeen:    cluster.lastSeen,
		})
	}

	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Occurrences != summaries[j].Occurrences {
			return summaries[i].Occurrences > summaries[j].Occurrences
		}
		return summaries[i].ID < summaries[j].ID
	})

	return summaries
}

func newClusterID(vector embed.Vector, sequence int) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(vector.Space))
	for _, value := range vector.Values {
		// Quantize so near-identical vectors that land in separate clusters
		// still produce readable, distinct IDs.
		_, _ = hash.Write([]byte{byte(int(value*100) & 0xff)})
	}
	_, _ = hash.Write([]byte{byte(sequence), byte(sequence >> 8)})

	return hex.EncodeToString(hash.Sum(nil))[:12]
}

func normalizeVector(values []float32) {
	var sum float64
	for _, value := range values {
		sum += float64(value) * float64(value)
	}
	if sum == 0 {
		return
	}

	scale := float32(1 / math.Sqrt(sum))
	for index := range values {
		values[index] *= scale
	}
}
