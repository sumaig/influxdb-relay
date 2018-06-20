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

func merge(a, b []byte) ([]byte, error) {
	r1 := new(Result)
	r2 := new(Result)

	err := json.Unmarshal(a, r1)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(b, r2)
	if err != nil {
		return nil, err
	}

	for _, v := range r1.Results {
		for _, vv := range r2.Results {
			if v.StatementID == vv.StatementID {
				v.Series = append(v.Series, vv.Series...)
			}
		}
	}

	c, err := json.Marshal(r1)
	if err != nil {
		return nil, err
	}

	return c, nil
}
