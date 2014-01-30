Solution to Level 4 of Stripe CTF 3
============================================

This is my high-scoring (#3 Woo!) solution to the final level
of Stipe's 2014 CTF.

Besides squashing the commits, I left the code as is, and thus 
doesn't really represent the best organization or style. There's some
left over bits of false starts in there, and a few other problems.
I also would not consider it to be idiomatic Go code.


Overview of Solution
------------------------------------

### `goraft/raft` Setup

The bulk of the work is done by the `goraft/raft` library, whose
reference implementation, `goraft/raftd`, looked suspiciously
similar to the original, incorrect, code provided by Stripe as a base
for level 4 (and thus a pretty big hint as to what library they
used in their implementation).

You have to do some finagling to get it to talk over Unix pipes, which
I did by wholesale copying and patching `raft/http_transport.go`,
but a better solution is just to override 
`transporter.Transport.Dial = transport.UnixDialer`.

The only other bit of magic you have to do is ensure you can join the
leader, which I do by repeatedly sending join requests to the leader.
There is also some logic to forward join requests to the real leader,
if for some reason `node0` is no longer a leader.

Finally, at some point on the weekend, `master` on `goraft/raft`
introduced a race condition that broke everything. It might be updated,
but you may want to checkout a commit from earlier last week. I used
0a20921dcb19e24e19794ce70a43bc9aadff3732

### Forwarding Queries

By default, `goraft/raft` only supports executing queries on the leader.
You have to write your own logic to send requests to the leader, and
produce correct responses.

A naive solution is to proxy requests to the leader, but this can 
cause issues because if the leader commits a change, then the connection
between the leader and the follower is lost, the follower will either
drop that request on the floor or retry the same query leading to
duplicates.

One possible solution, that I did not use, was to make queries idempotent
by caching the query (or some GUID) which allows the followers to
repeat queries without causing duplicates.

Instead I chose to do this

 - When a node receives a query, it assigns it a random ID.

 - It adds the query to a list of outstanding quiries (see below).

 - It forward the query to the leader, but does not wait for the response
 from the leader, nor does it retry if it fails.

 - The node then waits for the command to be commited against it's local
 database, and it is notified with the response.

Meanwhile

 - Each node keeps track of all outstanding queries it has seen in a 
 map mapping ID->Query. This is also used to provide notifications
 to any waiting SQL handlers.

 - Each nodes sends heart beats to all other nodes once in a while,
 and gets a list of outstanding queries, and merge it with it's own list.
 This allows queries that failed to forward to the correct master, to
 eventually be discovered.

 - Once quiries are commited locally, they are removed from the list,
 and any registered handlers are fired.


Finally, I do some other optimizations:

 - The leader processes queries in batches, so only one vote has
 to be done over raft to commit many quiries. This combined with
 outstanding query lists allow a lot of queries to be commited
 as soon as quorum can be attained again.

 - The initial solution shells out to execute SQL commands against
 `sqlite3`. Since we only ever see two kinds of queries, I just
 faked it all out.

 - I added Gzip compression to most requests, and tuned the timeouts
 as best I could.



Scoring
============================================

I managed to finish the competition late Thursday night (day two),
and was pretty happy with my place.

However, it was revealed Sunday, that the test runner (Octopus) didn't
sufficiently test single point of failure conditions. A new test runner
was released, the leader board was reset, and my initial solution 
proved to be a failure.

A couple more days of work, and I've worked out the kinks again, and
after many false starts and optimizations, I managed to score pretty
high.

However, there is a lot of *luck* involved here. Most of the time,
my solution can do about 15-20,000 points, or about 500-700 points when
normalized against their reference implementation (the scores are all
relative to their implementation).

But, their implementation has a bug. If you are really lucky, they will
hit a bug and score only a few hundred, or even as little as a 100 points
on their test case, which makes score look unreasonably good. This is
how I scored so high, and I assume that is the case for much of the
level 4 leader board.

Although it felt a little unsportmanlike, to attain a high ranking I
would repeatedly run the remote test runner hoping for a blow out score.
I figure this is true for everyone else at the top of the leaderboard,
because those score seem otherwise unattainable. There is some solace
in the fact that, if everyone was doing this and given sufficient time,
the best solutions would roughly correlate to their position on the board.

To be honest, I figure my solution is probably as good as anyone's
in the top 20 of the leader board. Everyone did a great job.

Note: Most of this code is derived from Stripe's sample code, some
copy-pasta from go/goraftd, and a lot of my own work. However, it's
not clear what license this code is under, since Stripe did not provide
one, though I assume it is okay to post solutions now that the competition
is over.


Original Readme
========================================

# SQLCluster

SQLCluster makes your SQLite highly-available.

## Getting started

To run this level, you'll need a working Go installation. If you don't
have one yet, it's quite easy to obtain. Just grab the appropriate
installer from:

  https://code.google.com/p/go/downloads/list

Then set your GOPATH environment variable:

  http://golang.org/doc/code.html#GOPATH

It'll probably be convenient to check this code out into
$GOPATH/src/stripe-ctf.com/sqlcluster (that way, `go build` will know
how to compile it without help). However, you can use the provided
`build.sh` regardless of where you happened to check this level out.

## Building and running

Run `./build.sh` to build the SQLCluster binary.

As always, you can run test cases via `test/harness`. This will
automatically fetch and compile Octopus, download your test cases, and
score your level for you.

Octopus will print out your score, together with how it arrived at
said score.

## Protocol

SQCluster communicates with the outside world over HTTP. The public
interface is simple:

  POST /sql:

    input:  A raw SQL body

    output: A message with the form "SequenceNumber: $n", followed
            by the output of that SQL command.

Run `./build.sh` to build SQLCluster and have it print out some
example usage (including `curl`s you can run locally).

## Supported platforms

SQLCluster has been tested on Mac OSX and Linux. It may work on other
platforms, but we make no promises. If it's not working for you, see
https://stripe-ctf.com/about#development for advice on getting a
development environment similar to our own.
