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

	for _, v1 := range r1.Results {
		for _, v2 := range r2.Results {
			if v1.StatementID == v2.StatementID {
				if len(v1.Series) == 0 || len(v2.Series) == 0 {
					v1.Series = append(v1.Series, v2.Series...)
				} else {
					v1.Series[0].Values = append(v1.Series[0].Values, v2.Series[0].Values...)
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
