package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	viper "github.com/spf13/viper"
	"google.golang.org/grpc"

	pbIntra "fedota/fl-coordinator/genproto/fl_intra"
	pbStatus "fedota/fl-coordinator/genproto/fl_status"
)

var start time.Time

// constants
const (
	varClientCheckin  = iota
	varSelectorFinish = iota
)


// to handle read writes
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


// server struct to implement gRPC server interface
type server struct {
	problemID           string
	flRootPath          string
	selectorAddress     string
	webserverAddress    string
	stage               pbStatus.Stages
	roundNo             int
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
	viper.AutomaticEnv()

	err := viper.ReadInConfig() // Find and read the config file
	if err != nil {             // Handle errors reading the config file
		panic(fmt.Errorf("Fatal error config file: %s", err))
	}
	//TODO: Add defaults for config using viper
}


func main() {
	// Enable line numbers in logging
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// if len(os.Args) != 2 {
	// 	log.Fatalln("Usage: go run ", os.Args[0], "<Coordinator Port>", "<FL Files Root>")
	// }

	port := ":" + viper.GetString("PORT")
	flRootPath := viper.GetString("FL_ROOT_PATH")
	selectorAddress := viper.GetString("SELECTOR_ADDRESS")
	webserverAddress := viper.GetString("WEBSERVER_ADDRESS")
	problemID := viper.GetString("PROBLEM_ID")

	// listen
	lis, err := net.Listen("tcp", port)
	check(err, "Failed to listen on port"+port)

	srv := grpc.NewServer()
	// server impl instance
	flCoordinator := &server{
		problemID:           problemID,
		flRootPath:          flRootPath,
		selectorAddress:     selectorAddress,
		webserverAddress:    webserverAddress,
		stage:               pbStatus.Stages_SELECTION,
		roundNo:             0,
		reads:               make(chan readOp),
		writes:              make(chan writeOp),
		selected:            make(chan bool),
		numClientCheckIns:   0,
		selectorCheckinList: make(map[string]bool),
		selectorFinishList:  make(map[string]bool),
	}

	// register FL intra and FL status server
	pbIntra.RegisterFlIntraServer(srv, flCoordinator)

	// go flServer.EventLoop()
	go flCoordinator.ConnectionHandler()

	// start serving
	log.Println("Starting server on port", port)
	err = srv.Serve(lis)
	check(err, "Failed to serve on port "+port)
}


// Handler for connection reads and updates to shared variables
// varClient check (when selector sends ping as it gets a new client connection)
// var selector fininsh
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
				if s.numClientCheckIns == viper.GetInt("CHECKIN_LIMIT") {
					log.Println("Cannot accept client as global count is already reached. Time:", time.Since(start))
					write.response <- false
				} else {
					if s.stage == pbStatus.Stages_COMPLETED {
						s.stage = pbStatus.Stages_SELECTION 
						// send status to webserver
						go s.sendRoundStatus()
					}
					// new client
					s.numClientCheckIns++
					// note selector id
					s.selectorCheckinList[write.strVal] = true
					log.Println("Handler ==> no. of selector checked in:", s.numClientCheckIns, "Time:", time.Since(start))
					log.Println("Handler ==> selector id:", write.strVal, "Time:", time.Since(start))
					log.Println("Handler ==> accepted", "Time:", time.Since(start))
					write.response <- true
				}
				
				// once limit is reaches with this request boadcast to all selectors
				// to start configuration stage 
				if s.numClientCheckIns == viper.GetInt("CHECKIN_LIMIT") {
					s.stage = pbStatus.Stages_CONFIGURATION 
					// send status to webserver
					go s.sendRoundStatus()
					// TODO: have to check status for it to be restarted if it fails 
					// or reset round after a number of fails
					go s.broadcastGoalCountReached()
				}

			case varSelectorFinish:
				if s.stage == pbStatus.Stages_CONFIGURATION {
					s.stage = pbStatus.Stages_REPORTING 
					// send status to webserver
					go s.sendRoundStatus()
				}

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
					s.stage = pbStatus.Stages_COMPLETED
					// send status to webserver
					go s.sendRoundStatus()
					s.resetFLVariables(true)
				}
			}
		}
	}
}


// Runs federated averaging
func (s *server) FederatedAveraging() {

	// model and checkpoint files
	completeInitPath := filepath.Join(s.flRootPath, viper.GetString("INIT_FILES_PATH"))
	checkpointFilePath := filepath.Join(completeInitPath, viper.GetString("CHECKPOINT_FILE"))
	modelFilePath := filepath.Join(completeInitPath, viper.GetString("MODEL_FILE"))
	var argsList []string
	// construct arguments requried for federated averaging 
	argsList = append(argsList, "federated_averaging.py", "--cf", checkpointFilePath, "--mf", modelFilePath, "--u")
	
	// get files locations and weight for the aggregation/averaging done by selectors
	for selectorID := range s.selectorFinishList {
		selectorFilePath := filepath.Join(s.flRootPath, selectorID)
		aggCheckpointFilePath := filepath.Join(selectorFilePath, viper.GetString("AGG_CHECKPOINT_FILE_PATH"))
		aggCheckpointWeightPath := filepath.Join(selectorFilePath, viper.GetString("AGG_CHECKPOINT_WEIGHT_PATH"))
		data, err := ioutil.ReadFile(aggCheckpointWeightPath)
		if err != nil {
			log.Println("FederatedAveraging: Unable to read checkpoint weight file. Time:", time.Since(start))
			log.Println(err)
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
		log.Println(err)
		return
	}
}


// selectors sends message to the coordinator with the new count of clients with them 
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

	// accept if count is lesser than limit
	return &pbIntra.FlClientStatus{Accepted: success}, nil
}


// broadcast to the selectors
func (s *server) broadcastGoalCountReached() {
	// for _, selector := range selectorAddresses {
	// with k8s dns will be able to send to all replicas
	selector := s.selectorAddress
	
	// create client
	var conn *grpc.ClientConn
	conn, err := grpc.Dial(selector, grpc.WithInsecure())

	if err != nil {
		log.Fatalf("Could not connect to %s: %s", selector, err)
		log.Println(err)
		return
	}
	defer conn.Close()

	// send broadcast
	c := pbIntra.NewFLGoalCountBroadcastClient(conn)
	_, err = c.GoalCountReached(context.Background(), &pbIntra.Empty{})

	if err != nil {
		log.Fatalf("Error sending to %s:  %s", selector, err)
		log.Println(err)
		return
	}
	log.Printf("Goal Count Reached message sent to %s", selector)
	// }
}


// receive pings from selectors to note that they have completed aggregation of weights send by respective clients
func (s *server) SelectorAggregationComplete(ctx context.Context, selectorID *pbIntra.SelectorId) (*pbIntra.Empty, error) {
	log.Println("Received mid averaging complete message from selector id:", selectorID.Id, " at : Time:", time.Since(start))

	// create a write operation
	write := writeOp{
		varType:  varSelectorFinish,
		strVal:   selectorID.Id,
		response: make(chan bool)}

	// send to handler (ConnectionHandler) via writes channel
	s.writes <- write

	// TODO: just <-write.response as we are not doing anything if response false 
	if !(<-write.response) {
		log.Println("Selector not considered for federated averaging process. Time:", time.Since(start))
	}

	// noted
	return &pbIntra.Empty{}, nil
}


// send round status to the webserver
func (s *server) sendRoundStatus() {
	// create client
	var conn *grpc.ClientConn
	conn, err := grpc.Dial(s.webserverAddress, grpc.WithInsecure())

	if err != nil {
		log.Fatalf("Could not connect to %s: %s", s.webserverAddress, err)
		log.Println(err)
		return
	}
	defer conn.Close()

	// send status
	c := pbStatus.NewFlStatusClient(conn)
	_, err = c.RoundStatus(context.Background(), &pbStatus.FlRoundStatus{
		Stage: s.stage,
		RoundNo: uint32(s.roundNo)})

	if err != nil {
		log.Fatalf("Error sending to %s:  %s", s.webserverAddress, err)
		log.Println(err)
		return
	}
	log.Printf("Status update message sent to %s", s.webserverAddress)
}


// reset round variables
func (s *server) resetFLVariables(complete bool) {
	s.stage = pbStatus.Stages_SELECTION
	if complete {
		s.roundNo++
	} else {
		// TODO remain same or reset to 0?
		// s.roundNo = 0;
	}
	s.numClientCheckIns = 0
	s.selectorCheckinList = make(map[string]bool)
	s.selectorFinishList = make(map[string]bool)
}


// Check for error, log and exit if err
func check(err error, errorMsg string) {
	if err != nil {
		log.Fatalf(errorMsg, " ==> ", err)
	}
}
