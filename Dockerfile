################## 1st Build Stage ####################
FROM golang:alpine AS builder
LABEL stage=builder

ENV GO_COORDINATOR_PATH "/go/src/federated-learning/fl-coordinator"

# Adding source files
ADD . ${GO_COORDINATOR_PATH}
WORKDIR ${GO_COORDINATOR_PATH}

# Build the GO program
RUN CGO_ENABLED=0 GOOS=linux go build -a

################## 2nd Build Stage ####################
FROM tensorflow/tensorflow:latest-py3 AS final

# FROM alpine AS final
RUN pip3 install --upgrade keras

WORKDIR /

# Copy from builder the GO executable file
COPY --from=builder ${GO_COORDINATOR_PATH} .

# Execute the program upon start 
CMD [ "./fl-coordinator" ]