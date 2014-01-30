package server

import (
	//"bufio"
	"bytes"
	"compress/gzip"
	//"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/goraft/raft"
	"github.com/gorilla/mux"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"path/filepath"
	"stripe-ctf.com/sqlcluster/log"
	"stripe-ctf.com/sqlcluster/sql"
	"stripe-ctf.com/sqlcluster/transport"
	"stripe-ctf.com/sqlcluster/util"
	"sync"
	"time"
)

var JOIN_TIMEOUT = 20 * time.Millisecond
var LEADER_HEALTH_CHECK_DELAY = 100 * time.Millisecond
var HEALTH_CHECK_DELAY = 100 * time.Millisecond
var PROCESS_QUEUE_DELAY = 25 * time.Millisecond
var HEALTH_CHECKER_MAIN_DELAY = 5 * time.Millisecond

func init() {
	raft.SetLogLevel(0)
}

// Encoding
func EncodeObjToResponse(w io.Writer, obj interface{}) error {
	compressed := gzip.NewWriter(w)

	enc := json.NewEncoder(compressed)
	err := enc.Encode(obj)

	if err != nil {
		return err
	}

	compressed.Close()

	return nil
}

func DecodeResponseToObj(r io.Reader, obj interface{}) error {
	decompressed, err := gzip.NewReader(r)

	if err != nil {
		return err
	}

	dec := json.NewDecoder(decompressed)

	err = dec.Decode(obj)

	if err != nil {
		return err
	}

	return nil
}

// Outstanding - Keeps track of outsanding queries to do
type Outstanding struct {
	queries map[string]*sql.Query
	notify  map[string]chan *sql.Output
	mutex   sync.Mutex
}

func NewOutstanding() *Outstanding {
	return &Outstanding{
		queries: map[string]*sql.Query{},
		notify:  map[string]chan *sql.Output{}}
}

func (o *Outstanding) Add(query *sql.Query) {
	o.mutex.Lock()
	defer o.mutex.Unlock()

	o.queries[query.Tag] = query
}

func (o *Outstanding) AddAndNotify(query *sql.Query) chan *sql.Output {
	o.mutex.Lock()
	defer o.mutex.Unlock()

	o.queries[query.Tag] = query

	notifyChan := make(chan *sql.Output, 1)

	o.notify[query.Tag] = notifyChan

	return notifyChan
}

func (o *Outstanding) Complete(output *sql.Output) {
	//log.Printf("Got Completion event for %v", output.Tag)
	o.mutex.Lock()

	delete(o.queries, output.Tag)

	notifyChan, valid := o.notify[output.Tag]

	if valid {
		delete(o.notify, output.Tag)
	}

	o.mutex.Unlock()

	if valid {
		//log.Printf("Sending completeion event for tag %v to listener", output.Tag)
		notifyChan <- output
	}
}

func (o *Outstanding) GetAll() []*sql.Query {
	o.mutex.Lock()
	defer o.mutex.Unlock()

	result := make([]*sql.Query, 0)
	for _, value := range o.queries {
		result = append(result, value)
	}

	return result
}

// DB -----------------------------------------------------
// A Wrapper for the SQL Database
type DB struct {
	sql    *sql.SQL
	notify chan *sql.Output
}

// Creates a new database.
func NewDB(path string) *DB {
	sqlPath := filepath.Join(path, "storage.sql")
	util.EnsureAbsent(sqlPath)

	return &DB{sql: sql.NewSQL(sqlPath), notify: make(chan *sql.Output)}
}

func (db *DB) Execute(query sql.Query) (*sql.Output, error) {
	//log.Printf(("Doing Query: tag: %v query: %#v", query.Tag, query.Statement)

	output, err := db.sql.Execute(query)

	return output, err
}

func (db *DB) ExecuteBatch(query []*sql.Query) []*sql.Output {
	//log.Printf(("Doing Query: tag: %v query: %#v", query.Tag, query.Statement)

	output := db.sql.ExecuteBatch(query)

	return output
}

// Commands --------------------------------------------
// Batch Command

// This command executes a batch of SQL queiries
type BatchCommand struct {
	Queries []*sql.Query `json:"queries"`
}

// Creates a new write command.
func NewBatchCommand(queries []*sql.Query) *BatchCommand {
	//log.Printf(("NewBatchCommand: %v %v", query.Tag, query.Statement)
	return &BatchCommand{
		Queries: queries,
	}
}

// The name of the command in the log.
func (c *BatchCommand) CommandName() string {
	return "batch"
}

// Writes a value to a key.
func (c *BatchCommand) Apply(server raft.Server) (interface{}, error) {
	//log.Printf(("Applying Batch Size: %v", len(c.Queries))
	db := server.Context().(*DB)

	for _, output := range db.ExecuteBatch(c.Queries) {
		db.notify <- output
	}

	// for _, query := range c.Queries {
	// 	output, err := db.Execute(*query)

	// 	if err != nil {
	// 		log.Fatal("Error applying command")
	// 		return nil, err
	// 	}

	// 	db.notify <- output

	// }
	return nil, nil
}

// Stuf ----------------------------------------------

type Server struct {
	name         string
	path         string
	listen       string
	router       *mux.Router
	raftServer   raft.Server
	httpServer   *http.Server
	db           *DB
	client       *transport.Client
	primary      string
	outstanding  *Outstanding
	joined       bool
	leaderNotify chan string
}

type HealthCheckResponse struct {
	Outstanding []*sql.Query `json:"outstanding"`
}

// Creates a new server.
func New(path, listen string) (*Server, error) {
	s := &Server{
		name:         listen,
		path:         path,
		listen:       listen,
		router:       mux.NewRouter(),
		client:       transport.NewClient(),
		db:           NewDB(path),
		outstanding:  NewOutstanding(),
		joined:       false,
		leaderNotify: make(chan string, 1000),
	}

	return s, nil
}

// Starts the server.
func (s *Server) ListenAndServe(primary string) error {
	var err error

	rand.Seed(int64(time.Now().Nanosecond()))

	s.primary = primary
	s.name = "name-" + s.listen

	raft.RegisterCommand(&BatchCommand{})

	// Initialize and start HTTP server.
	s.httpServer = &http.Server{
		Handler: s.router,
	}

	httpTransport := transport.NewClient().GetHTTPClient()

	//log.Printf(("Initializing Raft Server")
	transporter := NewHTTPTransporter("/raft", *httpTransport)
	s.raftServer, err = raft.NewServer(s.name, s.path, transporter, nil, s.db, "")
	if err != nil {
		log.Fatal(err)
	}
	transporter.Install(s.raftServer, s)
	s.raftServer.Start()

	s.raftServer.SetElectionTimeout(400 * time.Millisecond)

	s.raftServer.AddEventListener("addPeer", func(e raft.Event) {
		//log.Printf("Joined!")
		s.joined = true
	})

	s.raftServer.AddEventListener("leaderChange", func(e raft.Event) {
		leader := e.Value().(string)

		if leader == s.name {
			//log.Printf("Leader Changed to %v", leader)

			s.leaderNotify <- leader
		}
	})

	if primary == "" {
		cs, err := transport.Encode(s.listen)

		if err != nil {
			log.Fatal(err)
		}

		//log.Printf(("Starting as Leader")
		_, err = s.raftServer.Do(&raft.DefaultJoinCommand{
			Name:             s.raftServer.Name(),
			ConnectionString: cs,
		})
		//log.Printf(("I am Leader")

		if err != nil {
			log.Fatal(err)
		}
	} else {
		//log.Printf("Waiting 100 milliseconds to join Primary")
		time.AfterFunc(10*time.Millisecond, func() {
			maxTries := 25
			tries := 0
			for !s.joined {
				//log.Printf("Trying to Join")
				tries++
				//log.Printf("Attempting to Join")
				s.Join(primary)
				// if err != nil {
				// 	//log.Printf("Failed to join")
				// } else {
				// 	//log.Printf("Joined!")
				// 	break
				// }

				if tries > maxTries {
					log.Fatal("Could not join!")
				}

				time.Sleep(JOIN_TIMEOUT)
			}
		})
	}

	s.router.HandleFunc("/sql", s.sqlHandler).Methods("POST")
	s.router.HandleFunc("/healthcheck", s.healthcheckHandler).Methods("GET")
	s.router.HandleFunc("/join", s.joinHandler).Methods("POST")
	s.router.HandleFunc("/forward", s.forwardHandler).Methods("POST")

	// Start Unix transport
	l, err := transport.Listen(s.listen)
	if err != nil {
		log.Fatal(err)
	}

	//log.Printf(("Serving?")

	go s.healthChecker()
	go s.processQueue()
	go s.processNotifications()
	s.httpServer.Serve(l)
	return nil
}

func (s *Server) joinHandler(w http.ResponseWriter, req *http.Request) {
	//log.Printf("JOIN REQUEST")

	command := &raft.DefaultJoinCommand{}

	if err := json.NewDecoder(req.Body).Decode(&command); err != nil {
		//log.Printf("FAILED TO DECODE JOIN REQUEST")
		log.Fatal(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if s.raftServer.Leader() != s.name {
		err := s.ForwardJoin(command)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	//log.Printf(("Got join request from: %v", command)

	if _, err := s.raftServer.Do(command); err != nil {
		log.Fatal(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	//s.raftServer.AddPeer(command.Name, command.ConnectionString)
}

// Client operations

// Join an existing cluster
func (s *Server) Join(primary string) error {

	cs, err := transport.Encode(s.listen)
	if err != nil {
		return err
	}

	//log.Printf("My listen is: " + cs)

	csPrimary, err := transport.Encode(primary)
	if err != nil {
		return err
	}

	//log.Printf("My listen is: " + s.listen)
	//log.Printf("Attempting to Join: " + csPrimary)

	command := &raft.DefaultJoinCommand{
		Name:             s.raftServer.Name(),
		ConnectionString: cs,
	}

	b := util.JSONEncode(command)

	for {
		_, err := s.client.SafePost(csPrimary, "/join", "application/json", b)

		return err
	}
}

func (s *Server) GetLeaderCS() (string, error) {
	leaderName := s.raftServer.Leader()

	leaderPeer, valid := s.raftServer.Peers()[leaderName]

	if !valid {
		return "", errors.New("Can't find leader: " + leaderName)
	}

	//log.Printf(("Got Leader: %#v", leaderName)

	return leaderPeer.ConnectionString, nil
}

// Join an existing cluster
func (s *Server) ForwardJoin(command *raft.DefaultJoinCommand) error {
	//log.Printf("Fowarding Join Request: %v", command)

	csPrimary, err := s.GetLeaderCS()

	if err != nil {
		return err
	}

	b := util.JSONEncode(command)

	for {
		_, err := s.client.SafePost(csPrimary, "/join", "application/json", b)

		return err
	}
}

func (s *Server) queueAndWaitQuery(query *sql.Query) string {
	notifyChan := s.outstanding.AddAndNotify(query)

	output := <-notifyChan

	// Format
	formatted := fmt.Sprintf("SequenceNumber: %d\n%s",
		output.SequenceNumber, output.Stdout)

	return formatted
}

func (s *Server) queue(query *sql.Query) {
	s.outstanding.Add(query)
}

func (s *Server) mergeQueue(queries []*sql.Query) {
	for _, query := range queries {
		if !s.db.sql.IsCommited(*query) {
			s.queue(query)
		}
	}
}

func (s *Server) processNotifications() {
	for {
		//log.Printf("Processing Notifications.... Waiting")
		output := <-s.db.notify
		//log.Printf("Got notification that %#v has finished", output)
		s.outstanding.Complete(output)

	}
}

func (s *Server) processQueue() {
BlockLoop:
	for {
		// Block until we are the leader
		<-s.leaderNotify

		for {
			if s.raftServer.Leader() != s.name {
				continue BlockLoop
			}

			queries := s.outstanding.GetAll()

			if (s.raftServer.Leader() == s.name) && len(queries) > 0 {
				//start := time.Now()
				//log.Printf("I am Leader: Processing Outstanding (%v)", len(queries))

				s.raftServer.Do(NewBatchCommand(queries))
				//took := time.Since(start).Seconds()

				// if err != nil {
				// 	//log.Printf("Raft Error: %v", err.Error())
				// } else {
				// 	//log.Printf("Done Processing %v Queries in %0.2f (%0.2f per query)", len(queries), took, took/float64(len(queries)))

				// }
			}
			time.Sleep(PROCESS_QUEUE_DELAY)
		}
	}
}

// This is the only user-facing function, and accordingly the body is
// a raw string rather than JSON.
func (s *Server) sqlHandler(w http.ResponseWriter, req *http.Request) {
	//start := time.Now()
	query, err := ioutil.ReadAll(req.Body)
	if err != nil {
		//log.Printf(("Couldn't read body: %s", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// If not joined
	for !s.joined {
		time.Sleep(1 * time.Millisecond)
	}

	qry := &sql.Query{Tag: fmt.Sprintf("%v", rand.Intn(1000000000)),
		Statement: string(query)}

	//log.Printf(("Queing Query (%v): %#v", qry.Tag, string(query))

	go s.forwardQueryToLeader(qry)
	output := s.queueAndWaitQuery(qry)

	//log.Printf(("Query Done (%v): %#v", qry.Tag, string(query))
	//log.Printf(("Time Taken: %0.2fs", time.Now().Sub(start).Seconds())
	//log.Printf(("Returning Reponse:\n%v", output)
	//log.Printf(("Outstanding: %v", len(s.outstanding.GetAll()))

	w.Write([]byte(output))
}

func (s *Server) forwardQueryToLeader(query *sql.Query) {
	if s.raftServer.Leader() == s.name {
		// I am the leader, don't send it
		return
	}

	cs, err := s.GetLeaderCS()

	if err != nil {
		//log.Printf("Can't find leader to forward query")
		// Can't find leader
		return
	}

	var buffer bytes.Buffer
	compress := gzip.NewWriter(&buffer)

	err = EncodeObjToResponse(compress, query)

	compress.Close()

	if err != nil {
		log.Printf("Can't encode object to forward")
	}

	_, err = s.client.SafePost(cs, "/forward", "application/json", &buffer)

	if err != nil {
		log.Printf("Failed to forward query")
	}

}

func (s *Server) forwardHandler(w http.ResponseWriter, req *http.Request) {
	query := sql.Query{}

	decompress, err := gzip.NewReader(req.Body)

	if err != nil {
		//log.Printf("Error Decoding Gzipped Forwarded Query")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err = DecodeResponseToObj(decompress, &query)

	if err != nil {
		//log.Printf("Error Decoding Forwarded Query")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	//log.Printf("Got forwarded query: %#v", query)

	if !s.db.sql.IsCommited(query) {
		s.queue(&query)
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) healthcheckHandler(w http.ResponseWriter, req *http.Request) {
	response := &HealthCheckResponse{
		Outstanding: s.outstanding.GetAll(),
	}

	err := EncodeObjToResponse(w, response)

	if err != nil {
		log.Printf("healthCheckHandler ERror : %v", err.Error())
	}
}

func (s *Server) healthCheckPeer(peer *raft.Peer) {
	for {
		delay := HEALTH_CHECK_DELAY
		if s.raftServer.Leader() == s.name {
			delay = LEADER_HEALTH_CHECK_DELAY
		}

		// Endlessly
		response, err := s.client.SafeGet(peer.ConnectionString, "/healthcheck")

		if err != nil {
			continue
		}

		healthResponse := &HealthCheckResponse{}

		// if err := json.NewDecoder(response).Decode(&healthResponse); err != nil {
		// 	log.Fatal(err)
		// 	return
		// }
		err = DecodeResponseToObj(response, &healthResponse)
		if err != nil {
			log.Printf("healthCheckPeer Error: %v", err.Error())
			continue
		}

		s.mergeQueue(healthResponse.Outstanding)

		time.Sleep(delay)
	}
}

func (s *Server) healthChecker() {
	started := map[string]bool{}

	for {
		// Get Peers
		for name, peer := range s.raftServer.Peers() {
			// For Each New Peer Start Health Cheker
			if _, existing := started[name]; existing {
				continue
			}

			started[name] = true
			go s.healthCheckPeer(peer)
		}
		time.Sleep(HEALTH_CHECKER_MAIN_DELAY)
	}
}

// Raft stuff
func (s *Server) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	s.router.HandleFunc(pattern, handler)
}
