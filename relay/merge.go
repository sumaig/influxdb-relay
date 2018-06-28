package relay

import (
	"encoding/json"
)

type Result struct {
	Results []*data `json:"results"`
}

type data struct {
	StatementID int `json:"statement_id"`
	Series      []*series
}

type series struct {
	Name    string          `json:"name"`
	Columns []string        `json:"columns"`
	Values  [][]interface{} `json:"values"`
}

// 合并查询结果
func merge(n, o []byte) ([]byte, error) {
	if len(n) == 0 {
		return o, nil
	}

	if len(o) == 0 {
		return n, nil
	}

	r1 := new(Result)
	r2 := new(Result)

	err := json.Unmarshal(n, r1)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(o, r2)
	if err != nil {
		return nil, err
	}

	for _, v1 := range r1.Results {
		for _, v2 := range r2.Results {
			if v1.StatementID == v2.StatementID {
				if len(v1.Series) == 0 || len(v2.Series) == 0 {
					v1.Series = append(v1.Series, v2.Series...)
				} else {
					v1.Series[0].Values = mergeSlice(v1.Series[0].Values, v2.Series[0].Values)
				}
			}
		}
	}

	c, err := json.Marshal(r1)
	if err != nil {
		return nil, err
	}

	return c, nil
}

func mergeSlice(a, b [][]interface{}) [][]interface{} {
	if len(a) != len(b) {
		return a
	}

	for ai, av := range a {
		for bi, bv := range b {
			if ai == bi && av[0] == bv[0] {
				if av[1] == nil && bv[1] != nil {
					av[1] = bv[1]
				}
			}
		}
	}

	return a
}
