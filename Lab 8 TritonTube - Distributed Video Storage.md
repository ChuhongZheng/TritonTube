继续测试lab8，看文件的迁移

File Description

cmd/admin/main.go Storage admin tool (Lab 8)

cmd/storage/main.go Storage server CLI (Lab 8)

cmd/web/main.goWeb server CLI (Lab 7-9)

go.mod/go.sum Manages dependencies. For Lab 7, we will use SQLite through the mattn/go-sqlite3 driver.

internal/proto/ Generated files 

internal/storage/storage.go Storage server implementation (Lab 8)

internal/web/etcd.go Etcd metadata service (Lab 9)

internal/web/fs.go Local filesystem content service (Lab 7)

internal/web/interfaces.go Interface definitions

internal/web/nw.go Network content service (Lab 8)

internal/web/server.go Web server (Lab 7)

internal/web/sqlite.go SQLite metadata service (Lab 7)

internal/web/templates.go Web templates (Lab 7)

Makefile Command to compile protobuf

proto/admin.proto Protobuf definition for adding/removing nodes from the network content service (Lab 8)

# Lab 8: TritonTube - Distributed Video Storage

## Overview

Lab 8 builds on Lab 7 by introducing horizontal scaling to the video storage service using consistent hashing. Instead of storing video files and manifests on the local filesystem, the server now distributes video segments and other files across a set of video storage servers organized in a consistent hash ring. The system also supports dynamic scaling—when video storage servers are added or removed, uploaded videos are automatically relocated to the appropriate nodes to reflect the updated ring configuration.

## Startercode

The starter code for Lab 8 is the same as the one used in Lab 7, so you can build directly on top of your Lab 7 codebase.

## Networked Content Service

This section describes how TritonTube in this lab works as a whole. The system consists of three programs: video storage server, web server, and admin CLI (administration command line interface). These programs work together to handle the web app, video content, and distributed video storage.
There will be three primary changes from Lab 7. First, when a user uploads a video, after converting it to the MPEG-Dash format, instead of saving the files to the local filesystem using FSVideoContentService, we’ll use NWVideoContentService, which saves each file to one of the storage servers. NWVideoContentService determines the appropriate server using consistent hashing.

Second, when a user requests a video file by opening an individual video page, NWVideoContentService uses the same algorithm to determine which storage server has the file. It retrieves the file from the appropriate server and returns the content to the client.

Finally, the system supports adding/removing storage servers dynamically. These operations are done with the admin CLI. The web server will also run a gRPC server in the background. The admin CLI sends a request to the gRPC server running in tandem with the HTTP server. In addition to updating the list of storage servers, the web server needs to migrate files to the appropriate storage servers based on the updated consistent hash ring. This ensures that each video is stored on the correct node after the topology change. 
Note that the admin CLI **does not** start or stop storage servers. The administrator is responsible for starting the storage node before sending an add request and stopping the storage node after removing it from the cluster.

## Programs

### Storage Server

To start the system, you must first start a set of storage servers. ./cmd/storage/main.go is the entry point for the storage server program. The storage server takes two flags and one argument.

-host and -port specify the host and port number that the storage server uses. The two values are used to identify the storage server from the web server.The argument specifies the directory that the storage server uses for storing files.
go run ./cmd/storage -host localhost -port 8090 "./storage/8090"
For example, the command above starts a storage server at localhost:8090, and the server will use the directory ./storage/8090 to store files.
The web server will use this port to communicate with the storage server. The protocol between the web server and storage servers is not given—you need to design your .proto file.

### Web server

You will modify ./cmd/web/main.go to support the nw video content type. As a reminder, the web server entry point takes four positional arguments: METADATA_TYPE, METADATA_OPTIONS, CONTENT_TYPE, and CONTENT_OPTIONS. In addition to fs, which we implemented in Lab 7, its option (third argument), you will implement nw as a supported CONTENT_TYPE. When CONTENT_TYPE is set to nw, it takes a string in the following format as CONTENT_OPTIONS: adminhost:adminport,contenthost1:contentport1,contenthost2:contentport2,...

It is a comma-separated list of pairs of hosts and ports. The first entry specifies the host and the port that the web server uses for handling requests from the admin CLI. The remaining hosts and ports specify the list of storage servers.

For example, if the option is localhost:8081,localhost:8090,localhost:8091,localhost:8092, it means that the web server should start the gRPC server to communicate with the admin CLI on localhost:8081, and use three storage servers localhost:8090, localhost:8091, and localhost:8092 to store video content.

When the server needs to read or write a content file (to handle HTTP requests), it uses consistent hashing to determine the appropriate storage server from the list. It then communicates with the storage server using gRPC. Again, the protocol between the web server and storage servers is not given and you need to design one yourself.

When nw video content type is specified, in addition to serving the web pages, the web server is responsible for handling requests to add or remove nodes from the storage cluster. It runs the gRPC server to handle requests defined in admin.proto. When the web server receives a request to add or remove a node, it modifies its internal consistent hashing ring to maintain the invariants of the consistent hashing algorithm. 

### Admin CLI

./cmd/admin/main.go implements the admin CLI. This program is already implemented, so no modification is necessary in this file.
The admin CLI supports three operations:
list <server_address>add <server_address> <node_address>remove <server_address> <node_address>
<server_address> is the address that the gRPC server for admin.proto is running.
list shows the current list of nodes in the system. For example, if the admin server is running at localhost:8081, you can list the storage servers as follows:

\```$ go run ./cmd/admin list localhost:8081Storage cluster nodes: - localhost:8092 - localhost:8090 - localhost:8091```
In this case, there are three storage servers in the cluster.
add <server_address> <node_address> adds node_address to the web server running at server_address. Similarly, remove <server_address> <node_address> removes node_address from server_address.

When the node completes the request, it returns AddNodeResponse or RemoveNodeResponse. Those two messages have the migrated_file_count field that contains the number of files relocated as a result of adding/removing the node. 
For example, suppose we remove the node localhost:8090 from the cluster. We can run the following command:
\```$ go run ./cmd/admin remove localhost:8081 localhost:8090Successfully removed node: localhost:8090Number of files migrated: 3```
It indicates that localhost:8090 had three files, and they were moved to another node. We can add the same node back to the system with the following command:

\```$ go run ./cmd/admin add localhost:8081 localhost:8090Successfully added node: localhost:8090Number of files migrated: 3```
The three files are moved back to localhost:8090.
Note that the add and remove commands just modify the state of the web server. They do not start the server or stop the server—those operations have to be done by the administrator in addition to using the admin CLI to register/unregister storage servers.

## Consistent Hashing

### Determining the Storage Server

The web server distributed video content files using consistent hashing. See the 2nd half of Lecture 14 (May 15, 2025) for a better understanding of how consistent hashing works. In our implementation, we use 
:videoId/:filename as a key identifierhost:port as a node identifieruint_64 as an ID space0 - 18446744073709551615The first eight bytes of the SHA-256 digest interpreted as a big-endian uint64 as a hash function.
Use the following Go code as a hash function.
\```import (	"crypto/sha256"	"encoding/binary")
func hashStringToUint64(s string) uint64 {	sum := sha256.Sum256([]byte(s))	return binary.BigEndian.Uint64(sum[:8])}```
Let us walk through an example. Suppose that there are three nodes (localhost:8090, localhost:8091, and localhost:8092) in the system.

The web server maintains hash values of node identifiers in order to form a ring. First, we compute the hash value of each node ID.

| Node ID        | hashStringToUint64(Node ID) |
| -------------- | --------------------------- |
| localhost:8090 | 8831287181302866298         |
| localhost:8091 | 15195719212023434717        |
| localhost:8092 | 7885380174407825360         |

Then, we sort the list by the hash values.

| Node ID        | hashStringToUint64(Node ID) |
| -------------- | --------------------------- |
| localhost:8092 | 7885380174407825360         |
| localhost:8090 | 8831287181302866298         |
| localhost:8091 | 15195719212023434717        |

Visually, it would look like this on a ring.
Suppose that we want to store manifest.mpd for a video with PIKACHU as its video ID. The key identifier for this video file is PIKACHU/manifest.mpd. To determine which node to store the file, we use the same hash function to compute the hash of the key identifier. In this case, hashStringToUint64(“PIKACHU/manifest.mpd”) is 11664425278217952499. 

The value lies between the hashes of localhost:8090 and localhost:8091. Since the key is stored at its successor, PIKACHU/manifest.mpd would be stored at localhost:8091. If the key is mapped to a value bigger than the hash of localhost:8091, it would wrap around and be stored on the node with the smallest node ID (localhost:8092 in this case). If the key is mapped to one of the hashes of the nodes, the file is stored on that node.

## Implementation

### Distributing Files with Consistent Hashing

The first goal of the lab is to implement the NetworkVideoContentService in ./internal/web/nw.go. It should support the same Read and Write methods as Lab 7, but store files in one of the nodes. ./cmd/web/main.go parse the options and instantiate an instance of NetworkVideoContentService. Then, pass the instance to the web server.
You need to design the protocol between the web server and storage servers. We recommend creating a new .proto file inside ./proto directory similar to admin.proto. If you run make, it will generate the protobuf and gRPC server/client inside ./internal/proto. 
You must also implement the storage server program at ./cmd/storage/main.go. You may use ./internal/storage/storage.go (or any other file) to write the core logic of the server and import it from ./cmd/storage/main.go. 
NetworkVideoContentService should maintain the list of available nodes. To handle Read and Write requests, use consistent hashing to determine the storage node that the requested file belongs to, and use the client for that node to communicate with it.
Once it is done, make sure your website remains functional. You can use the testing procedures from Lab 7.

### Listing/Adding/Removing Nodes

The second goal of the lab is to support list/add/remove nodes operations. First, modify the NetworkVideoContentService to run a server for VideoContentAdminService defined in proto/admin.proto. This protobuf file defines the protocol used by the admin CLI and NetworkVideoContentService for listing/adding/removing nodes. The server must use the port and host specified as the first element of CONTENT_OPTIONS when starting the web server.
As mentioned above, the web server must maintain a consistent hash ring. When the server receives a ListNodesRequest, it should return the list of storage nodes in the cluster **in sorted order**.
When the server receives an AddNodeRequest or RemoveNodeRequest, it communicates with the storage servers to migrate necessary files. Count how many files are migrated, and respond to the admin CLI with AddNodeResponse or RemoveNodeRequest with migrated_file_count.

### Notes

You may assume that the addresses are valid when handling AddNodeRequest and RemoveNodeRequest. For AddNodeRequest, you may assume that there is a node running at the specified address, and the address is not part of the cluster. For RemoveNodeRequest, you may assume that the specified address is in the system.

You may assume that the number of storage nodes does not go below one. In other words, we will not issue a “remove” request when only one storage server is in the cluster. In such cases, you may return an error, but we will not test them.

Respond to AddNodeRequest and RemoveNodeRequest after all file migrations are done. We will not test the website during the migration (after sending a request and before getting a response). However, the website should be functional once the admin CLI receives a response.

Each file must be stored at exactly one node. In particular, during adding or removing nodes, there may be a moment when the same file is copied to multiple nodes. However, after the add/remove request is complete, the copies must be removed.

There will be no concurrent add/remove requests. Only one add/remove request is issued at a time.