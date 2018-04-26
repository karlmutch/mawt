package mawt

// This file contains a function that when started will listen for status
// messages for the home portal and will update a data structure that another
// function checks for on a regular basis and uses to update LEDs etc attached
// to one or more fadecandy device(s)

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/go-stack/stack"
	"github.com/karlmutch/errors"
	// colorful "github.com/lucasb-eyer/go-colorful"

	"github.com/cnf/structhash"

	"github.com/kellydunn/go-opc"
)

type LastStatus struct {
	status *Status
	sync.Mutex
}

type FadeCandy struct {
	oc *opc.Client
}

// This file contains the implementation of a listener for tecthulhu events that will on
// a regular basis lift the last known state of the portal and will update the fade-candy as needed

func StartFadeCandy(server string, subscribeC chan chan *PortalMsg, errorC chan<- errors.Error, quitC <-chan struct{}) (fc *FadeCandy) {

	statusC := make(chan *PortalMsg, 1)
	subscribeC <- statusC

	status := &LastStatus{}

	go func() {
		defer close(statusC)
		for {
			select {
			case msg := <-statusC:
				if nil == msg {
					continue
				}
				if msg.Home {
					status.Lock()
					status.status = msg.Status.DeepCopy()
					status.Unlock()
				}
			case <-quitC:
				return
			}
		}
	}()

	fc = &FadeCandy{}

	go fc.run(status, server, time.Duration(200*time.Millisecond), errorC, quitC)

	return fc
}

func (fc *FadeCandy) run(status *LastStatus, server string, refresh time.Duration, errorC chan<- errors.Error, quitC <-chan struct{}) {

	last := []byte{}

	if fc.oc == nil {
		fc.oc = opc.NewClient()
	}

	if errGo := fc.oc.Connect("tcp", server); errGo != nil {

		fc.oc = nil

		err := errors.Wrap(errGo).With("url", server).With("stack", stack.Trace().TrimRuntime())

		select {
		case errorC <- err:
		case <-time.After(100 * time.Millisecond):
			fmt.Fprintln(os.Stderr, err.Error())
		}
	}

	// Start the LED command message pusher
	go fc.RunLoop(errorC, quitC)

	//sr, err := GetSeqRunner()
	//if err != nil {
	//	return err
	//}

	for {
		select {
		case <-time.After(refresh):
			status.Lock()
			copied := status.status.DeepCopy()
			status.Unlock()

			hash := structhash.Md5(copied, 1)
			if bytes.Compare(last, hash) != 0 {
				last = hash
				// TODO Call InitSequence  and then load an effect that is applied
				// to a specific universe, instead of the hard coded test we have below
				//seq := Sequence{
				//	Steps: []Step{},
				//}
				//sr.InitSequence(seq, time.now())
				if err := test8LED(0.15, copied); err != nil {
					select {
					case errorC <- err.With("url", server):
					case <-time.After(100 * time.Millisecond):
						fmt.Fprintln(os.Stderr, err.Error())
					}
				}
			}
		case <-quitC:
			return
		}
	}
}

func (fc *FadeCandy) Send(m *opc.Message) (err errors.Error) {
	if fc.oc == nil {
		return errors.New("fadecandy server not online").With("stack", stack.Trace().TrimRuntime())
	}

	if m == nil {
		return errors.New("invalid message").With("stack", stack.Trace().TrimRuntime())
	}

	if errGo := fc.oc.Send(m); errGo != nil {
		return errors.Wrap(errGo).With("stack", stack.Trace().TrimRuntime())
	}
	return nil
}

func (fc *FadeCandy) RunLoop(errorC chan<- errors.Error, quitC <-chan struct{}) (err errors.Error) {

	defer close(errorC)

	sr, err := GetSeqRunner()
	if err != nil {
		return err
	}

	devices, universes, err := GetUniverses()
	if err != nil {
		return err
	}

	refresh := time.Duration(30 * time.Millisecond)
	for {
		select {
		case <-time.After(refresh):
			// Populate the logical buffers
			sr.ProcessFrame(time.Now())

			// Copy the logical buffers into the physical buffers

			for _, id := range universes {
				devices.UpdateUniverse(id, sr.UniverseData(id))
			}

			// Iterate across physical strands sending updates to the
			// FadeCandies, possibly diffing to previous frame to see if necessary
			deviceStrands, errGo := GetStrands()
			if errGo != nil {
				sendErr(errorC, errors.Wrap(errGo).With("stack", stack.Trace().TrimRuntime()))
				continue
			}
			for device, strands := range deviceStrands {
				strandNum := 0
				for strand, strandLen := range strands {
					strandNum++
					if strandLen == 0 {
						continue
					}
					// The following gives us an array of RGBA as a linear arrangement of LEDs
					// for the indicated strand
					strandData, errGo := devices.GetStrandData(uint(device), uint(strand))
					if errGo != nil {
						sendErr(errorC, errors.Wrap(errGo).With("stack", stack.Trace().TrimRuntime()))
						continue
					}

					strip := fmt.Sprintf("%02d → ", strandNum)
					// Prepare a message for this strand that has 3 bytes per LED
					m := opc.NewMessage(0)
					m.SetLength(uint16(len(strandData) * 3))
					for i, rgba := range strandData {
						r, g, b, a := rgba.RGBA()
						if a == 0 {
							r = 0
							g = 0
							b = 0
						}
						strip += fmt.Sprintf("%s[38;2;%d;%d;%dm█", "\x1b", uint8(r), uint8(g), uint8(b))
						m.SetPixelColor(i, uint8(r), uint8(g), uint8(b))
					}
					if err := fc.Send(m); err != nil {
						// sendErr(errorC, err)
						fmt.Println(strip)
						// After a fatal error reduce the frequency of the refresh
						refresh = time.Duration(5 * time.Second)
						// See if we can print some RGB Values
						continue
					}
					refresh = time.Duration(30 * time.Millisecond)
				}
			}
		case <-quitC:
			return
		}
	}
}

func sendErr(errorC chan<- errors.Error, err errors.Error) {
	if errorC == nil {
		return
	}
	select {
	case errorC <- err:
	case <-time.After(20 * time.Millisecond):
		fmt.Println(fmt.Sprintf("%+v", err.Error()))
	}
}
