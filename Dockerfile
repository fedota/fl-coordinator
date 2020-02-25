ARG GO_COORDINATOR_PATH="/go/src/federated-learning/fl-coordinator"

################## 1st Build Stage ####################
FROM golang:alpine AS builder
LABEL stage=builder

ARG GO_COORDINATOR_PATH

# Adding source files
ADD . ${GO_COORDINATOR_PATH}
WORKDIR ${GO_COORDINATOR_PATH}

ENV GO111MODULE=on

# Cache go mods based on go.sum/go.mod files
RUN go mod download

# Build the GO program
RUN CGO_ENABLED=0 GOOS=linux go build -a -o server

################## 2nd Build Stage ####################
FROM tensorflow/tensorflow:latest-py3 AS final

RUN pip3 install --upgrade keras

ARG GO_COORDINATOR_PATH

# Copy from builder the GO executable file
COPY --from=builder ${GO_COORDINATOR_PATH}/server .
COPY --from=builder ${GO_COORDINATOR_PATH}/config.yaml .
COPY --from=builder ${GO_COORDINATOR_PATH}/federated_averaging.py .

# Execute the program upon start 
CMD ["./server"]