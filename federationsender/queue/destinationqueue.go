// Copyright 2017 Vector Creations Ltd
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package queue

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/matrix-org/dendrite/federationsender/types"
	"github.com/matrix-org/gomatrixserverlib"
	log "github.com/sirupsen/logrus"
)

// destinationQueue is a queue of events for a single destination.
// It is responsible for sending the events to the destination and
// ensures that only one request is in flight to a given destination
// at a time.
type destinationQueue struct {
	parent             *OutgoingQueues
	client             *gomatrixserverlib.FederationClient
	origin             gomatrixserverlib.ServerName
	destination        gomatrixserverlib.ServerName
	runningMutex       sync.Mutex
	running            bool                              // protected by runningMutex
	sentCounter        int                               // protected by runningMutex
	lastTransactionIDs []gomatrixserverlib.TransactionID // protected by runningMutex
	pendingEvents      []*types.PendingPDU               // protected by runningMutex
	pendingEDUs        []*types.PendingEDU               // protected by runningMutex
}

// Send event adds the event to the pending queue for the destination.
// If the queue is empty then it starts a background goroutine to
// start sending events to that destination.
func (oq *destinationQueue) sendEvent(ev *types.PendingPDU) {
	oq.runningMutex.Lock()
	defer oq.runningMutex.Unlock()
	oq.pendingEvents = append(oq.pendingEvents, ev)
	if !oq.running {
		oq.running = true
		go oq.backgroundSend()
	}
}

// sendEDU adds the EDU event to the pending queue for the destination.
// If the queue is empty then it starts a background goroutine to
// start sending event to that destination.
func (oq *destinationQueue) sendEDU(e *types.PendingEDU) {
	oq.runningMutex.Lock()
	defer oq.runningMutex.Unlock()
	oq.pendingEDUs = append(oq.pendingEDUs, e)
	if !oq.running {
		oq.running = true
		go oq.backgroundSend()
	}
}

func (oq *destinationQueue) backgroundSend() {
	for {
		t := oq.next()
		if t == nil {
			// If the queue is empty then stop processing for this destination.
			oq.parent.queuesMutex.Lock()
			delete(oq.parent.queues, oq.destination)
			oq.parent.queuesMutex.Unlock()
			return
		}

		// TODO: blacklist uncooperative servers.

		_, err := oq.client.SendTransaction(context.TODO(), *t)
		if err != nil {
			log.WithFields(log.Fields{
				"destination": oq.destination,
				log.ErrorKey:  err,
			}).Info("problem sending transaction")

			for _, pdu := range (*t).PDUs {
				if err := oq.parent.QueueEvent((*t).Destination, pdu); err != nil {
					log.WithFields(log.Fields{
						"destination": (*t).Destination,
						log.ErrorKey:  err,
					}).Warn("Error queuing PDU")
				}
			}
		}
	}
}

// next creates a new transaction from the pending event queue
// and flushes the queue.
// Returns nil if the queue was empty.
func (oq *destinationQueue) next() *gomatrixserverlib.Transaction {
	oq.runningMutex.Lock()
	defer oq.runningMutex.Unlock()

	if len(oq.pendingEvents) == 0 && len(oq.pendingEDUs) == 0 {
		oq.running = false
		return nil
	}

	var t gomatrixserverlib.Transaction
	now := gomatrixserverlib.AsTimestamp(time.Now())
	t.TransactionID = gomatrixserverlib.TransactionID(fmt.Sprintf("%d-%d", now, oq.sentCounter))
	t.Origin = oq.origin
	t.Destination = oq.destination
	t.OriginServerTS = now
	t.PreviousIDs = oq.lastTransactionIDs
	if t.PreviousIDs == nil {
		t.PreviousIDs = []gomatrixserverlib.TransactionID{}
	}

	oq.lastTransactionIDs = []gomatrixserverlib.TransactionID{t.TransactionID}

	for _, pdu := range oq.pendingEvents {
		t.PDUs = append(t.PDUs, *pdu.PDU)
	}
	oq.pendingEvents = nil
	oq.sentCounter += len(t.PDUs)

	for _, edu := range oq.pendingEDUs {
		t.EDUs = append(t.EDUs, *edu.EDU)
	}
	oq.pendingEDUs = nil
	oq.sentCounter += len(t.EDUs)

	return &t
}
