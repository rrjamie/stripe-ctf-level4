package sql

import (
	//"bytes"
	//"os/exec"
	//"strings"
	//"stripe-ctf.com/sqlcluster/log"
	"sync"
	//"syscall"
	//"time"
	//	"errors"
	"fmt"
	"regexp"
	"strconv"
)

type Columns struct {
	name         string
	friendCount  int
	requestCount int
	favoriteWord string
}

func (c *Columns) toString() string {
	return fmt.Sprintf("%v|%v|%v|%v", c.name, c.friendCount, c.requestCount, c.favoriteWord)
}

type Output struct {
	Tag            string
	Stdout         []byte
	Stderr         []byte
	SequenceNumber int
}

type Query struct {
	Tag       string `json:"Tag"`
	Statement string `json:"Statement"`
}

type SQL struct {
	sequenceNumber int
	mutex          sync.Mutex
	data           map[string]*Columns
	results        map[string]*Output
	initialized    bool
}

func NewSQL(path string) *SQL {
	data := map[string]*Columns{}

	data["siddarth"] = &Columns{name: "siddarth"}
	data["gdb"] = &Columns{name: "gdb"}
	data["christian"] = &Columns{name: "christian"}
	data["andy"] = &Columns{name: "andy"}
	data["carl"] = &Columns{name: "carl"}

	sql := &SQL{
		data:    data,
		results: map[string]*Output{}}
	return sql
}

func (sql *SQL) toString() string {
	return fmt.Sprintf("%v\n%v\n%v\n%v\n%v\n",
		sql.data["siddarth"].toString(),
		sql.data["gdb"].toString(),
		sql.data["christian"].toString(),
		sql.data["andy"].toString(),
		sql.data["carl"].toString())
}

func (sql *SQL) IsCommited(query Query) bool {
	sql.mutex.Lock()
	defer sql.mutex.Unlock()

	_, valid := sql.results[query.Tag]

	return valid
}

func (sql *SQL) GetCommitted(query Query) *Output {
	sql.mutex.Lock()
	defer sql.mutex.Unlock()

	output, _ := sql.results[query.Tag]

	return output
}

func (sql *SQL) Execute(query Query) (*Output, error) {
	sql.mutex.Lock()
	defer sql.mutex.Unlock()

	return sql.executeQuery(query)
}

func (sql *SQL) ExecuteBatch(queries []*Query) []*Output {
	sql.mutex.Lock()
	defer sql.mutex.Unlock()

	results := make([]*Output, len(queries))

	for ix, query := range queries {
		results[ix], _ = sql.executeQuery(*query)
	}

	return results
}

func (sql *SQL) executeQuery(query Query) (*Output, error) {
	// No Locks

	tag := query.Tag
	command := query.Statement

	if result, valid := sql.results[tag]; valid {
		return result, nil
	}

	defer func() { sql.sequenceNumber += 1 }()

	// UPDATE ctf3 SET friendCount=friendCount+17, requestCount=requestCount+1, favoriteWord="mphjkmkamdanfmg" WHERE name="siddarth"
	queryRe := regexp.MustCompile(`UPDATE ctf3 SET friendCount=friendCount\+(\d+), requestCount=requestCount\+(\d+), favoriteWord="(\w+)" WHERE name="(\w+)"`)
	var fCount int64
	var rCount int64
	favWord := ""
	name := ""

	if queryRe.MatchString(command) {
		groups := queryRe.FindStringSubmatch(command)
		fCount, _ = strconv.ParseInt(groups[1], 10, 32)
		rCount, _ = strconv.ParseInt(groups[2], 10, 32)
		favWord = groups[3]
		name = groups[4]
	} else {
		o := ""
		if sql.initialized {
			o = "Error: near line 1: table ctf3 already exists\nError: near line 6: UNIQUE constraint failed: ctf3.name\n"
			//o = "Error: near line 1: table ctf3 already exists\nError: near line 6: column name is not unique\n"
		}
		sql.initialized = true

		// Initialization Query, no ouput
		output := &Output{
			Tag:            query.Tag,
			Stdout:         []byte(o),
			Stderr:         []byte(""),
			SequenceNumber: sql.sequenceNumber,
		}
		sql.results[tag] = output
		return output, nil
	}

	// Update stuff
	col := sql.data[name]
	col.friendCount += int(fCount)
	col.requestCount += int(rCount)
	col.favoriteWord = favWord

	o := sql.toString()
	e := ""

	output := &Output{
		Tag:            query.Tag,
		Stdout:         []byte(o),
		Stderr:         []byte(e),
		SequenceNumber: sql.sequenceNumber,
	}

	sql.results[tag] = output

	return output, nil
}
