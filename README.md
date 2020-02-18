# fl-coordinator
Master Aggregator and Coordinator (MAC) for the Federated Learning system

- Compile protobuf needed in fl-misc by `fl-proto.sh` script
- Create go modules dependencies files by `go mod init`
- Build the docker image:
`docker build -t fl-coordinator .`

- Run the container using:
`docker run --rm --name fl-coordinator -p 50050:50050 fl-coordinator`

- To inspect the container, open bash using:
`docker run -it fl-coordinator bash`