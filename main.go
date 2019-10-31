package main

import (
	"context"
	pbIntra "federated-learning/fl-coordinator/genproto/fl_intra"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"
)

var start time.Time

var selectorAddresses = []string{"localhost:50051"}

// constants
const (
	port                        = ":50050"
	modelFilePath               = "./data/model/model.h5"
	checkpointFilePath          = "./data/checkpoint/fl_checkpoint"
	weightUpdatesDir            = "./data/weight_updates/"
	chunkSize                   = 64 * 1024
	postCheckinReconnectionTime = 8000
	postUpdateReconnectionTime  = 8000
	estimatedRoundTime          = 8000
	estimatedWaitingTime        = 20000
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
	log.Println("Starting server on port", port)
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
				if s.numCheckIns == checkinLimit {
					log.Println("Cannot accept client as global count is already reached. Time:", time.Since(start))
					write.response <- false
				} else {
					s.numCheckIns++
					log.Println("Handler ==> numCheckIns", s.numCheckIns, "Time:", time.Since(start))
					log.Println("Handler ==> accepted", "Time:", time.Since(start))
					write.response <- true
				}

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

// Sends true if the client is accepted, false if global count was already reached
func (s *server) ClientCountUpdate(ctx context.Context, clientCount *pbIntra.ClientCount) (*pbIntra.FlClientStatus, error) {

	log.Println("Received client request with id:%s : Time:", clientCount.Id, time.Since(start))

	// create a write operation
	write := writeOp{
		varType:  VAR_NUM_CHECKINS,
		response: make(chan bool)}

	// send to handler (ConnectionHandler) via writes channel
	s.writes <- write

	success := <-write.response

	if success && s.numCheckIns == checkinLimit {
		go broadcastGoalCountReached()
	}

	return &pbIntra.FlClientStatus{Accepted: success}, nil
}

func broadcastGoalCountReached() {
	for _, selector := range selectorAddresses {
		var conn *grpc.ClientConn
		conn, err := grpc.Dial(selector, grpc.WithInsecure())

		if err != nil {
			log.Fatalf("Could not connect to %s: %s", selector, err)
		}
		defer conn.Close()

		c := pbIntra.NewFLGoalCountBroadcastClient(conn)
		_, err = c.GoalCountReached(context.Background(), &pbIntra.Empty{})

		if err != nil {
			log.Fatalf("Error sending to %s:  %s", selector, err)
		}
		log.Printf("Goal Count Reached message sent to %s", selector)
	}
}

func (s *server) Update(stream pbIntra.FlIntra_UpdateServer) error {

	var checkpointWeight int64

	log.Println("Update Request: Time:", time.Since(start))

	// create a write operation
	write := writeOp{
		varType:  VAR_NUM_UPDATES_START,
		response: make(chan bool)}
	// send to handler (ConnectionHandler) via writes channel
	s.writes <- write

	// IMPORTANT
	// after sending write request, we wait for response as handler is sending true and gets blocked
	// if this read is not present -> handler and all subsequent rpc calls are in deadlock
	if !(<-write.response) {
		log.Println("Update Request Accepted: Time:", time.Since(start))
	}

	// create read operation
	read := readOp{
		varType:  VAR_NUM_UPDATES_START,
		response: make(chan int)}
	// send to handler (ConnectionHandler) via reads channel
	// log.Println("Read Request: Time:", time.Since(start))
	s.reads <- read

	// log.Println("Waiting Read Response: Time:", time.Since(start))
	// get index to differentiate clients
	index := <-read.response

	log.Println("Index : ", index)

	// open the file
	// log.Println(weightUpdatesDir + strconv.Itoa(index))
	filePath := weightUpdatesDir + "weight_updates_" + strconv.Itoa(index)
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY, os.ModeAppend)
	check(err, "Unable to open new checkpoint file")
	defer file.Close()

	for {
		// receive Fl data
		flData, err := stream.Recv()
		// exit after data transfer completes
		if err == io.EOF {

			// create a write operation
			write = writeOp{
				varType:  VAR_NUM_UPDATES_FINISH,
				response: make(chan bool)}
			// send to handler (ConnectionHandler) via writes channel
			s.writes <- write

			// put result in map
			// TODO: modify by making a go-routine to do updates
			s.mu.Lock()
			s.checkpointUpdates[index] = flRoundClientResult{
				checkpointWeight:   checkpointWeight,
				checkpointFilePath: filePath,
			}
			log.Println("Checkpoint Update: ", s.checkpointUpdates[index])
			s.mu.Unlock()

			if !(<-write.response) {
				log.Println("Checkpoint Update confirmed. Time:", time.Since(start))
			}

			return stream.SendAndClose(&pbIntra.FlData{
				IntVal: postUpdateReconnectionTime,
				Type:   pbIntra.Type_FL_RECONN_TIME,
			})
		}
		check(err, "Unable to receive update data from client")

		if flData.Type == pbIntra.Type_FL_CHECKPOINT_UPDATE {
			// write data to file
			_, err = file.Write(flData.Message.Content)
			check(err, "Unable to write into new checkpoint file")
		} else if flData.Type == pbIntra.Type_FL_CHECKPOINT_WEIGHT {
			checkpointWeight = flData.IntVal
		}
	}

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
