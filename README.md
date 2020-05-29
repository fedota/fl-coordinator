# Coordinator
Master Aggregator and Coordinator (MAC) for the Federated Learning system

## Overview
Coordinator has the following responsibilities
- Work with Selectors to determine whether the required client count for starting the round has been reached.
- Give the signal for Selectors to go ahead with Configuration stage in federated learning
- After Selectors have completed aggregation of weights from their respective client (Reporting stage) the coordinator will perform the final aggregation and update the weights of the global model
- Inform status of the round to the Webserver

It has access to the following contents in the shared directory (more information: [fedota-infra](https://github.com/fedota/fedota-infra))
```
\data 
	\initFiles
		fl_checkpoint <- W 
		model.h5 <- R
		.
		.
	\<selector-id>
		fl_agg_checkpoint <- R
		fl_agg_checkpoint_weight <- R
		.
		.
	.
	.
	.
```

### Workflow
- In the Selection stage, it received a ping from the Selector, handled by a separate go-routine via gRPC with the current client count, once the Selector has received a connection from the client.
- An update is made via the ConnectionHandler to the client count variable using channels to avoid inconsistencies. If the goal count for clients has not been reached conditional acceptance is back to the go routine and hence to the Selector (to hold the client with it) essentially using FCFS for now. 
- If the goal count has reached and any more client connections come in, they are rejected by the connection handler and dropped by the respective Selector.
- When the goal count is met, it sends a message to the Selectors holding client connections, instructing them to being Configuration and then updates the stage.
- Once Selectors send a ping, noting they have completed aggregating weights from client, Reporting stage beings. After all the Selectors have done so, the final federated averaging process starts using those Selector aggregated weights
- Federated averaging uses the files aggregated checkpoint and aggregated weight files of Selectors as shown above and updates/writes the result to global checkpoint in `initFiles`
- Each stage update is reported to the Webserver

## Setup 
1. Compile protobuf needed in [fl-misc](https://github.com/fedota/fl-misc) by `fl-proto.sh` script
2. Build the docker image:
	`docker build -t fedota/fl-coordinator .`

- Run the container using:
`docker run --rm --name coord -p 50050:50050 -v /path/to/shared/dir:/data -v /path_to/config.yaml:/server_dir/config.yaml fedota/fl-coordinator` \
If running *fl-coordinator* and *fl-selector* locally replace `-p 50050:50050` with `--network="host"`\
For example, `docker run --rm --name coord --network="host" -v $PWD/../data:/data -v $PWD/config.yaml:/server_dir/config.yaml fedota/fl-coordinator` 

- To inspect the running container, open bash using:
`docker exec -t -i coord /bin/bash`

- To simply run and inspect a new container, execute:
`docker run -it fedota/fl-coordinator bash`

[Optional] Install dependencies files by `go test`

## Resources
-  Mark McGranaghan: https://gobyexample.com/stateful-goroutines