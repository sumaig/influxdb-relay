package relay

import (
	"fmt"
	"testing"
)

func TestMerge(t *testing.T) {
	a := `{
    "results": [
        {
            "statement_id": 0,
            "series": [
                {
                    "name": "cpu_load_short",
                    "columns": [
                        "time",
                        "value"
                    ],
                    "values": [
                        [
                            "2015-01-29T21:55:43.702900257Z",
                            null
                        ],
                        [
                            "2015-01-29T21:55:43.702900257Z",
                            0.55
                        ],
                        [
                            "2015-06-11T20:46:02Z",
                            0.64
                        ]
                    ]
                }
            ]
        }
    ]
}`

	b := `{
    "results": [
        {
            "statement_id": 0,
            "series": [
                {
                    "name": "cpu_load_short",
                    "columns": [
                        "time",
                        "value"
                    ],
                    "values": [
                        [
                            "2015-01-29T21:55:43.702900257Z",
                            2
                        ],
                        [
                            "2015-01-29T21:55:43.702900257Z",
                            0.58
                        ],
                        [
                            "2015-06-11T20:46:02Z",
                            null
                        ]
                    ]
                }
            ]
        }
    ]
}`

	c := `{
    "results": [
        {
            "statement_id": 0
        }
    ]
}`

	ab, err := merge([]byte(a), []byte(b))
	if err != nil {
		t.Error(err)
	}
	fmt.Println(string(ab))

	ac, err := merge([]byte(a), []byte(c))
	if err != nil {
		t.Error()
	}
	fmt.Println(string(ac))

	bc, err := merge([]byte(b), []byte(c))
	if err != nil {
		t.Error()
	}
	fmt.Println(string(bc))

}
