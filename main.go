package main

import (
	"context"
	pbIntra "federated-learning/fl-coordinator/genproto/fl_intra"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"

	viper "github.com/spf13/viper"
	"google.golang.org/grpc"
)

var start time.Time
var port string

// TODO: Selector addresses should be sent from selector along with ID
var selectorAddresses = []string{"localhost:50051"}

// constants
const (
	varClientCheckin  = iota
	varSelectorFinish = iota
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
	strVal   string
	val      int
	response chan bool
}

// server struct to implement gRPC Round service interface
type server struct {
	flRootPath          string
	reads               chan readOp
	writes              chan writeOp
	selected            chan bool
	numClientCheckIns   int
	selectorCheckinList map[string]bool
	selectorFinishList  map[string]bool
}

func init() {
	start = time.Now()

	viper.SetConfigName("config") // name of config file (without extension)
	viper.AddConfigPath(".")      // optionally look for config in the working directory
	err := viper.ReadInConfig()   // Find and read the config file
	if err != nil {               // Handle errors reading the config file
		panic(fmt.Errorf("Fatal error config file: %s", err))
	}
	// TODO: Add defaults for config using viper
}

func main() {
	// Enable line numbers in logging
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// if len(os.Args) != 2 {
	// 	log.Fatalln("Usage: go run ", os.Args[0], "<Coordinator Port>", "<FL Files Root>")
	// }

	port = ":" + viper.GetString("coordinator-port")
	flRootPath := viper.GetString("fl-root-path")

	// listen
	lis, err := net.Listen("tcp", port)
	check(err, "Failed to listen on port"+port)

	srv := grpc.NewServer()
	// server impl instance
	flServerCoordinator := &server{
		flRootPath:          flRootPath,
		reads:               make(chan readOp),
		writes:              make(chan writeOp),
		selected:            make(chan bool),
		numClientCheckIns:   0,
		selectorCheckinList: make(map[string]bool),
		selectorFinishList:  make(map[string]bool),
	}
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
			case varClientCheckin:
				read.response <- s.numClientCheckIns
			case varSelectorFinish:
				read.response <- len(s.selectorFinishList)
			}
		// write query
		case write := <-s.writes:
			log.Println("Handler ==> Write Query:", write.varType, "Time:", time.Since(start))
			switch write.varType {
			case varClientCheckin:
				if s.numClientCheckIns == viper.GetInt("checkin-limit") {
					log.Println("Cannot accept client as global count is already reached. Time:", time.Since(start))
					write.response <- false
				} else {
					s.numClientCheckIns++
					s.selectorCheckinList[write.strVal] = true
					log.Println("Handler ==> no. of selector checked in:", s.numClientCheckIns, "Time:", time.Since(start))
					log.Println("Handler ==> selector id:", write.strVal, "Time:", time.Since(start))
					log.Println("Handler ==> accepted", "Time:", time.Since(start))
					write.response <- true
				}

			case varSelectorFinish:
				// If selector participated in FL round, i.e if selector Id is present in list
				if _, idFound := s.selectorCheckinList[write.strVal]; idFound {
					s.selectorFinishList[write.strVal] = true
					log.Println("Handler ==> no. of selectors having completed mid-averaging: ", len(s.selectorFinishList), "Finish Time:", time.Since(start))
					log.Println("Handler ==> accepted update", "Time:", time.Since(start))
					write.response <- true
				} else {
					write.response <- false
				}

				// if enough updates available, start FA
				if len(s.selectorFinishList) == len(s.selectorCheckinList) {
					// begin federated averaging process
					log.Println("Begin Federated Averaging Process")
					s.FederatedAveraging()
					s.resetFLVariables()
				}
			}
		// After wait period check if everything is fine
		case <-time.After(time.Duration(viper.GetInt64("estimated-waiting-time")) * time.Second):
			log.Println("Update Handler ==> Timeout", "Time:", time.Since(start))
			// if checkin limit is not reached
			// abandon round
			// TODO: after checkin is done

			// TODO: Decide about updates not received in time
		}
	}
}

// Runs federated averaging
func (s *server) FederatedAveraging() {

	completeInitPath := s.flRootPath + viper.GetString("init-files-path")
	checkpointFilePath := completeInitPath + viper.GetString("checkpoint-file")
	modelFilePath := completeInitPath + viper.GetString("model-file")
	var argsList []string
	argsList = append(argsList, "federated_averaging.py", "--cf", checkpointFilePath, "--mf", modelFilePath, "--u")
	for selectorID := range s.selectorFinishList {
		selectorFilePath := s.flRootPath + selectorID
		aggCheckpointFilePath := selectorFilePath + viper.GetString("agg-checkpoint-file-path")
		aggCheckpointWeightPath := selectorFilePath + viper.GetString("agg-checkpoint-weight-path")
		data, err := ioutil.ReadFile(aggCheckpointWeightPath)
		if err != nil {
			log.Println("FederatedAveraging: Unable to read checkpoint weight file. Time:", time.Since(start))
			return
		}
		checkpointWeight, _ := strconv.ParseInt(string(data), 10, 64)
		argsList = append(argsList, strconv.FormatInt(checkpointWeight, 10), aggCheckpointFilePath)
	}

	log.Println("Arguments passed to federated averaging python file: ", argsList)

	// model path
	cmd := exec.Command("python", argsList...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		log.Println("FederatedAveraging ==> Unable to run federated averaging. Time:", time.Since(start))
		return
	}
}

// Sends true if the client is accepted, false if global count was already reached
func (s *server) ClientCountUpdate(ctx context.Context, clientCount *pbIntra.ClientCount) (*pbIntra.FlClientStatus, error) {

	log.Println("Received client request from selector id:", clientCount.Id, " at : Time:", time.Since(start))

	// create a write operation
	write := writeOp{
		varType:  varClientCheckin,
		strVal:   clientCount.Id,
		response: make(chan bool)}

	// send to handler (ConnectionHandler) via writes channel
	s.writes <- write

	success := <-write.response

	if success && s.numClientCheckIns == viper.GetInt("checkin-limit") {
		go s.broadcastGoalCountReached()
	}

	return &pbIntra.FlClientStatus{Accepted: success}, nil
}

func (s *server) broadcastGoalCountReached() {
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

func (s *server) SelectorAggregationComplete(ctx context.Context, selectorID *pbIntra.SelectorId) (*pbIntra.Empty, error) {
	log.Println("Received mid averaging complete message from selector id:", selectorID.Id, " at : Time:", time.Since(start))

	// create a write operation
	write := writeOp{
		varType:  varSelectorFinish,
		strVal:   selectorID.Id,
		response: make(chan bool)}

	// send to handler (ConnectionHandler) via writes channel
	s.writes <- write

	if !(<-write.response) {
		log.Println("Selector not considered for federated averaging process. Time:", time.Since(start))
	}

	return &pbIntra.Empty{}, nil
}

// Check for error, log and exit if err
func check(err error, errorMsg string) {
	if err != nil {
		log.Fatalf(errorMsg, " ==> ", err)
	}
}

func (s *server) resetFLVariables() {
	s.numClientCheckIns = 0
	s.selectorCheckinList = make(map[string]bool)
	s.selectorFinishList = make(map[string]bool)
}
