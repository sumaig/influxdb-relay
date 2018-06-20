package consistent

import (
	"hash/crc32"
	"sort"
	"strconv"
	"sync"
)

type Hash func(data []byte) uint32

type Map struct {
	hash     Hash
	replicas int
	nodes    []int // Sorted
	hashMap  map[int]string
	sync.RWMutex
}

func New(replicas int, fn Hash) *Map {
	m := &Map{
		replicas: replicas,
		hash:     fn,
		hashMap:  make(map[int]string),
	}
	if m.hash == nil {
		m.hash = crc32.ChecksumIEEE
	}
	return m
}

// Returns true if there are no items available.
func (m *Map) IsEmpty() bool {
	return len(m.nodes) == 0
}

// Adds some nodes to the hash.
func (m *Map) Add(nodes ...string) {
	m.Lock()
	defer m.Unlock()
	for _, node := range nodes {
		for i := 0; i < m.replicas; i++ {
			hash := int(m.hash([]byte(strconv.Itoa(i) + node)))
			m.nodes = append(m.nodes, hash)
			m.hashMap[hash] = node
		}
	}
	sort.Ints(m.nodes)
}

// Remove some nodes from the hash
func (m *Map) Remove(nodes ...string) {
	m.Lock()
	defer m.Unlock()
	for _, node := range nodes {
		for i := 0; i < m.replicas; i++ {
			hash := int(m.hash([]byte(strconv.Itoa(i) + node)))
			delete(m.hashMap, hash)
			m.deleteSlice(hash)
		}
	}
}

// delete element from nodes slice
func (m *Map) deleteSlice(hash int) {
	for i := 0; i < len(m.nodes); i++ {
		if m.nodes[i] == hash {
			m.nodes = append(m.nodes[:i], m.nodes[i+1:]...)
		}
	}
}

// Gets the closest item in the hash to the provided key.
func (m *Map) Get(key string) string {
	m.RLock()
	defer m.RUnlock()
	if m.IsEmpty() {
		return ""
	}

	hash := int(m.hash([]byte(key)))

	// Binary search for appropriate replica.
	idx := sort.Search(len(m.nodes), func(i int) bool { return m.nodes[i] >= hash })

	// Means we have cycled back to the first replica.
	if idx == len(m.nodes) {
		idx = 0
	}

	return m.hashMap[m.nodes[idx]]
}
