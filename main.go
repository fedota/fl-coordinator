package main

import (
	"context"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	pbIntra "federated-learning/fl-selector/genproto/fl_intra"

	"google.golang.org/grpc"
)

var start time.Time

// constants
const (
	port = ":50051"
	modelFilePath               = "./data/model/model.h5"
	checkpointFilePath          = "./data/checkpoint/fl_checkpoint"
	weightUpdatesDir            = "./data/weight_updates/"
	chunkSize                   = 64 * 1024
	postCheckinReconnectionTime = 8000
	postUpdateReconnectionTime  = 8000
	estimatedRoundTime          = 8000
	estimatedWaitingTime = 20000
	checkinLimit                = 3
	VAR_NUM_CHECKINS            = 0
	VAR_NUM_UPDATES_START       = 1
	VAR_NUM_UPDATES_FINISH      = 2
)

// store the result from a client
type flRoundClientResult struct {
	checkpointWeight   int64
	checkpointFilePath string
}

// to handle read writes
// Credit: Mark McGranaghan
// Source: https://gobyexample.com/stateful-goroutines
type readOp struct {
	varType  int
	response chan int
}
type writeOp struct {
	varType  int
	val      int
	response chan bool
}

// server struct to implement gRPC Round service interface
type server struct {
	reads             chan readOp
	writes            chan writeOp
	selected          chan bool
	numCheckIns       int
	numUpdatesStart   int
	numUpdatesFinish  int
	mu                sync.Mutex
	checkpointUpdates map[int]flRoundClientResult
}

func init() {
	start = time.Now()
}

func main() {
	// listen
	lis, err := net.Listen("tcp", port)
	check(err, "Failed to listen on port"+port)

	srv := grpc.NewServer()
	// server impl instance
	flServerCoordinator := &server{
		numCheckIns:       0,
		numUpdatesStart:   0,
		numUpdatesFinish:  0,
		checkpointUpdates: make(map[int]flRoundClientResult),
		reads:             make(chan readOp),
		writes:            make(chan writeOp),
		selected:          make(chan bool)}
	// register FL intra server
	pbIntra.RegisterFlIntraServer(srv, flServerCoordinator)

	// go flServer.EventLoop()
	go flServerCoordinator.ConnectionHandler()

	// start serving
	err = srv.Serve(lis)
	check(err, "Failed to serve on port "+port)
}

// Handler for connection reads and updates
// Takes care of update and checkin limits
// Credit: Mark McGranaghan
// Source: https://gobyexample.com/stateful-goroutines
func (s *server) ConnectionHandler() {
	for {
		select {
		// read query
		case read := <-s.reads:
			log.Println("Handler ==> Read Query:", read.varType, "Time:", time.Since(start))
			switch read.varType {
			case VAR_NUM_CHECKINS:
				read.response <- s.numCheckIns
			case VAR_NUM_UPDATES_START:
				read.response <- s.numUpdatesStart
			case VAR_NUM_UPDATES_FINISH:
				read.response <- s.numUpdatesFinish
			}
		// write query
		case write := <-s.writes:
			log.Println("Handler ==> Write Query:", write.varType, "Time:", time.Since(start))
			switch write.varType {
			case VAR_NUM_CHECKINS:
				s.numCheckIns++
				log.Println("Handler ==> numCheckIns", s.numCheckIns, "Time:", time.Since(start))
				log.Println("Handler ==> accepted", "Time:", time.Since(start))
				write.response <- true
			case VAR_NUM_UPDATES_START:
				s.numUpdatesStart++
				log.Println("Handler ==> numUpdates", s.numUpdatesStart, "Time:", time.Since(start))
				log.Println("Handler ==> accepted update", "Time:", time.Since(start))
				write.response <- true

			case VAR_NUM_UPDATES_FINISH:
				s.numUpdatesFinish++
				log.Println("Handler ==> numUpdates: ", s.numUpdatesFinish, "Finish Time:", time.Since(start))
				log.Println("Handler ==> accepted update", "Time:", time.Since(start))
				write.response <- true

				// if enough updates available, start FA
				if s.numUpdatesFinish == s.numUpdatesStart {
					// begin federated averaging process
					log.Println("Begin Federated Averaging Process")
					s.FederatedAveraging()
					s.resetFLVariables()
				}
			}
		// After wait period check if everything is fine
		case <-time.After(estimatedWaitingTime * time.Second):
			log.Println("Timeout")
			// if checkin limit is not reached
			// abandon round
			// TODO: after checkin is done
			
			// TODO: Decide about updates not received in time
		}
	}
}

// Runs federated averaging
func (s *server) FederatedAveraging() {

	var argsList []string
	argsList = append(argsList, "federated_averaging.py", "--cf", checkpointFilePath, "--mf", modelFilePath, "--u")
	for _, v := range s.checkpointUpdates {
		argsList = append(argsList, strconv.FormatInt(v.checkpointWeight, 10), v.checkpointFilePath)
	}

	log.Println("Arguments passed to federated averaging python file: ", argsList)

	// model path
	cmd := exec.Command("python", argsList...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	check(err, "Unable to run federated averaging")
}

func (s *server) ClientCountUpdate(ctx context.Context, clientCount *pbIntra.ClientCount) (*pbIntra.Empty, error) {
	return &pbIntra.Empty{}, nil
}

func (s *server) BeginConfiguration(ctx context.Context, empty *pbIntra.Empty) (*pbIntra.Empty, error) {
	return &pbIntra.Empty{}, nil
}

// Check for error, log and exit if err
func check(err error, errorMsg string) {
	if err != nil {
		log.Fatalf(errorMsg, " ==> ", err)
	}
}

func (s *server) resetFLVariables() {
	s.numCheckIns = 0
	s.numUpdatesStart = 0
	s.numUpdatesFinish = 0
	s.checkpointUpdates = make(map[int]flRoundClientResult)
}
