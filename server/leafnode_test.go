// Copyright 2019 The NATS Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

type captureLeafNodeRandomIPLogger struct {
	DummyLogger
	ch  chan struct{}
	ips [3]int
}

func (c *captureLeafNodeRandomIPLogger) Debugf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	if strings.Contains(msg, "hostname_to_resolve") {
		ippos := strings.Index(msg, "127.0.0.")
		if ippos != -1 {
			n := int(msg[ippos+8] - '1')
			c.ips[n]++
			for _, v := range c.ips {
				if v < 2 {
					return
				}
			}
			// All IPs got at least some hit, we are done.
			c.ch <- struct{}{}
		}
	}
}

func TestLeafNodeRandomIP(t *testing.T) {
	u, err := url.Parse("nats://hostname_to_resolve:1234")
	if err != nil {
		t.Fatalf("Error parsing: %v", err)
	}

	resolver := &myDummyDNSResolver{ips: []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"}}

	o := DefaultOptions()
	o.Host = "127.0.0.1"
	o.Port = -1
	o.LeafNode.Port = 0
	o.LeafNode.Remotes = []*RemoteLeafOpts{{URLs: []*url.URL{u}}}
	o.LeafNode.ReconnectInterval = 50 * time.Millisecond
	o.LeafNode.resolver = resolver
	o.LeafNode.dialTimeout = 15 * time.Millisecond
	s := RunServer(o)
	defer s.Shutdown()

	l := &captureLeafNodeRandomIPLogger{ch: make(chan struct{})}
	s.SetLogger(l, true, true)

	select {
	case <-l.ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("Does not seem to have used random IPs")
	}
}

type testLoopbackResolver struct{}

func (r *testLoopbackResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	return []string{"127.0.0.1"}, nil
}

func TestLeafNodeTLSWithCerts(t *testing.T) {
	conf1 := createConfFile(t, []byte(`
		port: -1
		leaf {
			listen: "127.0.0.1:-1"
			tls {
				ca_file: "../test/configs/certs/tlsauth/ca.pem"
				cert_file: "../test/configs/certs/tlsauth/server.pem"
				key_file:  "../test/configs/certs/tlsauth/server-key.pem"
				timeout: 2
			}
		}
	`))
	defer os.Remove(conf1)
	s1, o1 := RunServerWithConfig(conf1)
	defer s1.Shutdown()

	u, err := url.Parse(fmt.Sprintf("nats://localhost:%d", o1.LeafNode.Port))
	if err != nil {
		t.Fatalf("Error parsing url: %v", err)
	}
	conf2 := createConfFile(t, []byte(fmt.Sprintf(`
		port: -1
		leaf {
			remotes [
				{
					url: "%s"
					tls {
						ca_file: "../test/configs/certs/tlsauth/ca.pem"
						cert_file: "../test/configs/certs/tlsauth/client.pem"
						key_file:  "../test/configs/certs/tlsauth/client-key.pem"
						timeout: 2
					}
				}
			]
		}
	`, u.String())))
	defer os.Remove(conf2)
	o2, err := ProcessConfigFile(conf2)
	if err != nil {
		t.Fatalf("Error processing config file: %v", err)
	}
	o2.NoLog, o2.NoSigs = true, true
	o2.LeafNode.resolver = &testLoopbackResolver{}
	s2 := RunServer(o2)
	defer s2.Shutdown()

	checkFor(t, 3*time.Second, 10*time.Millisecond, func() error {
		if nln := s1.NumLeafNodes(); nln != 1 {
			return fmt.Errorf("Number of leaf nodes is %d", nln)
		}
		return nil
	})
}

func TestLeafNodeTLSRemoteWithNoCerts(t *testing.T) {
	conf1 := createConfFile(t, []byte(`
		port: -1
		leaf {
			listen: "127.0.0.1:-1"
			tls {
				ca_file: "../test/configs/certs/tlsauth/ca.pem"
				cert_file: "../test/configs/certs/tlsauth/server.pem"
				key_file:  "../test/configs/certs/tlsauth/server-key.pem"
				timeout: 2
			}
		}
	`))
	defer os.Remove(conf1)
	s1, o1 := RunServerWithConfig(conf1)
	defer s1.Shutdown()

	u, err := url.Parse(fmt.Sprintf("nats://localhost:%d", o1.LeafNode.Port))
	if err != nil {
		t.Fatalf("Error parsing url: %v", err)
	}
	conf2 := createConfFile(t, []byte(fmt.Sprintf(`
		port: -1
		leaf {
			remotes [
				{
					url: "%s"
					tls {
						ca_file: "../test/configs/certs/tlsauth/ca.pem"
						timeout: 5
					}
				}
			]
		}
	`, u.String())))
	defer os.Remove(conf2)
	o2, err := ProcessConfigFile(conf2)
	if err != nil {
		t.Fatalf("Error processing config file: %v", err)
	}

	if len(o2.LeafNode.Remotes) == 0 {
		t.Fatal("Expected at least a single leaf remote")
	}

	var (
		got      float64 = o2.LeafNode.Remotes[0].TLSTimeout
		expected float64 = 5
	)
	if got != expected {
		t.Fatalf("Expected %v, got: %v", expected, got)
	}
	o2.NoLog, o2.NoSigs = true, true
	o2.LeafNode.resolver = &testLoopbackResolver{}
	s2 := RunServer(o2)
	defer s2.Shutdown()

	checkFor(t, 3*time.Second, 10*time.Millisecond, func() error {
		if nln := s1.NumLeafNodes(); nln != 1 {
			return fmt.Errorf("Number of leaf nodes is %d", nln)
		}
		return nil
	})

	// Here we only process options without starting the server
	// and without a root CA for the remote.
	conf3 := createConfFile(t, []byte(fmt.Sprintf(`
		port: -1
		leaf {
			remotes [
				{
					url: "%s"
					tls {
						timeout: 10
					}
				}
			]
		}
	`, u.String())))
	defer os.Remove(conf3)
	o3, err := ProcessConfigFile(conf3)
	if err != nil {
		t.Fatalf("Error processing config file: %v", err)
	}

	if len(o3.LeafNode.Remotes) == 0 {
		t.Fatal("Expected at least a single leaf remote")
	}
	got = o3.LeafNode.Remotes[0].TLSTimeout
	expected = 10
	if got != expected {
		t.Fatalf("Expected %v, got: %v", expected, got)
	}

	// Here we only process options without starting the server
	// and check the default for leafnode remotes.
	conf4 := createConfFile(t, []byte(fmt.Sprintf(`
		port: -1
		leaf {
			remotes [
				{
					url: "%s"
					tls {
						ca_file: "../test/configs/certs/tlsauth/ca.pem"
					}
				}
			]
		}
	`, u.String())))
	defer os.Remove(conf4)
	o4, err := ProcessConfigFile(conf4)
	if err != nil {
		t.Fatalf("Error processing config file: %v", err)
	}

	if len(o4.LeafNode.Remotes) == 0 {
		t.Fatal("Expected at least a single leaf remote")
	}
	got = o4.LeafNode.Remotes[0].TLSTimeout
	expected = float64(DEFAULT_LEAF_TLS_TIMEOUT)
	if int(got) != int(expected) {
		t.Fatalf("Expected %v, got: %v", expected, got)
	}
}

type captureErrorLogger struct {
	DummyLogger
	errCh chan string
}

func (l *captureErrorLogger) Errorf(format string, v ...interface{}) {
	select {
	case l.errCh <- fmt.Sprintf(format, v...):
	default:
	}
}

func TestLeafNodeAccountNotFound(t *testing.T) {
	ob := DefaultOptions()
	ob.LeafNode.Host = "127.0.0.1"
	ob.LeafNode.Port = -1
	sb := RunServer(ob)
	defer sb.Shutdown()

	u, _ := url.Parse(fmt.Sprintf("nats://127.0.0.1:%d", ob.LeafNode.Port))

	logFileName := createConfFile(t, []byte(""))
	defer os.Remove(logFileName)

	oa := DefaultOptions()
	oa.LeafNode.ReconnectInterval = 15 * time.Millisecond
	oa.LeafNode.Remotes = []*RemoteLeafOpts{
		{
			LocalAccount: "foo",
			URLs:         []*url.URL{u},
		},
	}
	// Expected to fail
	if _, err := NewServer(oa); err == nil || !strings.Contains(err.Error(), "local account") {
		t.Fatalf("Expected server to fail with error about no local account, got %v", err)
	}
	oa.Accounts = []*Account{NewAccount("foo")}
	sa := RunServer(oa)
	defer sa.Shutdown()

	l := &captureErrorLogger{errCh: make(chan string, 1)}
	sa.SetLogger(l, false, false)

	checkLeafNodeConnected(t, sa)

	// Now simulate account is removed with config reload, or it expires.
	sa.accounts.Delete("foo")

	// Restart B (with same Port)
	sb.Shutdown()
	sb = RunServer(ob)
	defer sb.Shutdown()

	// Wait for report of error
	select {
	case e := <-l.errCh:
		if !strings.Contains(e, "No local account") {
			t.Fatalf("Expected error about no local account, got %s", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Did not get the error")
	}

	// For now, sa would try to recreate the connection for ever.
	// Check that lid is increasing...
	time.Sleep(100 * time.Millisecond)
	lid := atomic.LoadUint64(&sa.gcid)
	if lid < 4 {
		t.Fatalf("Seems like connection was not retried")
	}
}

// This test ensures that we can connect using proper user/password
// to a LN URL that was discovered through the INFO protocol.
func TestLeafNodeBasicAuthFailover(t *testing.T) {
	content := `
	listen: "127.0.0.1:-1"
	cluster {
		listen: "127.0.0.1:-1"
		%s
	}
	leafnodes {
		listen: "127.0.0.1:-1"
		authorization {
			user: foo
			password: pwd
			timeout: 1
		}
	}
	`
	conf := createConfFile(t, []byte(fmt.Sprintf(content, "")))
	defer os.Remove(conf)

	sb1, ob1 := RunServerWithConfig(conf)
	defer sb1.Shutdown()

	conf = createConfFile(t, []byte(fmt.Sprintf(content, fmt.Sprintf("routes: [nats://127.0.0.1:%d]", ob1.Cluster.Port))))
	defer os.Remove(conf)

	sb2, _ := RunServerWithConfig(conf)
	defer sb2.Shutdown()

	checkClusterFormed(t, sb1, sb2)

	content = `
	port: -1
	accounts {
		foo {}
	}
	leafnodes {
		listen: "127.0.0.1:-1"
		remotes [
			{
				account: "foo"
				url: "nats://foo:pwd@127.0.0.1:%d"
			}
		]
	}
	`
	conf = createConfFile(t, []byte(fmt.Sprintf(content, ob1.LeafNode.Port)))
	defer os.Remove(conf)

	sa, _ := RunServerWithConfig(conf)
	defer sa.Shutdown()

	checkLeafNodeConnected(t, sa)

	// Shutdown sb1, sa should reconnect to sb2
	sb1.Shutdown()

	// Wait a bit to make sure there was a disconnect and attempt to reconnect.
	time.Sleep(250 * time.Millisecond)

	// Should be able to reconnect
	checkLeafNodeConnected(t, sa)
}

func TestLeafNodeRTT(t *testing.T) {
	ob := DefaultOptions()
	ob.PingInterval = 15 * time.Millisecond
	ob.LeafNode.Host = "127.0.0.1"
	ob.LeafNode.Port = -1
	sb := RunServer(ob)
	defer sb.Shutdown()

	lnBURL, _ := url.Parse(fmt.Sprintf("nats://127.0.0.1:%d", ob.LeafNode.Port))
	oa := DefaultOptions()
	oa.PingInterval = 15 * time.Millisecond
	oa.LeafNode.Remotes = []*RemoteLeafOpts{{URLs: []*url.URL{lnBURL}}}
	sa := RunServer(oa)
	defer sa.Shutdown()

	checkLeafNodeConnected(t, sa)

	checkRTT := func(t *testing.T, s *Server) time.Duration {
		t.Helper()
		var ln *client
		s.mu.Lock()
		for _, l := range s.leafs {
			ln = l
			break
		}
		s.mu.Unlock()
		var rtt time.Duration
		checkFor(t, 2*firstPingInterval, 15*time.Millisecond, func() error {
			ln.mu.Lock()
			rtt = ln.rtt
			ln.mu.Unlock()
			if rtt == 0 {
				return fmt.Errorf("RTT not tracked")
			}
			return nil
		})
		return rtt
	}

	prevA := checkRTT(t, sa)
	prevB := checkRTT(t, sb)

	// Wait to see if RTT is updated
	checkUpdated := func(t *testing.T, s *Server, prev time.Duration) {
		attempts := 0
		timeout := time.Now().Add(2 * firstPingInterval)
		for time.Now().Before(timeout) {
			if rtt := checkRTT(t, s); rtt != prev {
				return
			}
			attempts++
			if attempts == 5 {
				s.mu.Lock()
				for _, ln := range s.leafs {
					ln.mu.Lock()
					ln.rtt = 0
					ln.mu.Unlock()
					break
				}
				s.mu.Unlock()
			}
			time.Sleep(15 * time.Millisecond)
		}
		t.Fatalf("RTT probably not updated")
	}
	checkUpdated(t, sa, prevA)
	checkUpdated(t, sb, prevB)

	sa.Shutdown()
	sb.Shutdown()

	// Now check that initial RTT is computed prior to first PingInterval
	// Get new options to avoid possible race changing the ping interval.
	ob = DefaultOptions()
	ob.PingInterval = time.Minute
	ob.LeafNode.Host = "127.0.0.1"
	ob.LeafNode.Port = -1
	sb = RunServer(ob)
	defer sb.Shutdown()

	lnBURL, _ = url.Parse(fmt.Sprintf("nats://127.0.0.1:%d", ob.LeafNode.Port))
	oa = DefaultOptions()
	oa.PingInterval = time.Minute
	oa.LeafNode.Remotes = []*RemoteLeafOpts{{URLs: []*url.URL{lnBURL}}}
	sa = RunServer(oa)
	defer sa.Shutdown()

	checkLeafNodeConnected(t, sa)

	checkRTT(t, sa)
	checkRTT(t, sb)
}

func TestLeafNodeValidateAuthOptions(t *testing.T) {
	opts := DefaultOptions()
	opts.LeafNode.Username = "user1"
	opts.LeafNode.Password = "pwd"
	opts.LeafNode.Users = []*User{&User{Username: "user", Password: "pwd"}}
	if _, err := NewServer(opts); err == nil || !strings.Contains(err.Error(),
		"can not have a single user/pass and a users array") {
		t.Fatalf("Expected error about mixing single/multi users, got %v", err)
	}

	// Check duplicate user names
	opts.LeafNode.Username = _EMPTY_
	opts.LeafNode.Password = _EMPTY_
	opts.LeafNode.Users = append(opts.LeafNode.Users, &User{Username: "user", Password: "pwd"})
	if _, err := NewServer(opts); err == nil || !strings.Contains(err.Error(), "duplicate user") {
		t.Fatalf("Expected error about duplicate user, got %v", err)
	}
}

func TestLeafNodeBasicAuthSingleton(t *testing.T) {
	opts := DefaultOptions()
	opts.LeafNode.Port = -1
	opts.LeafNode.Account = "unknown"
	if s, err := NewServer(opts); err == nil || !strings.Contains(err.Error(), "cannot find") {
		if s != nil {
			s.Shutdown()
		}
		t.Fatalf("Expected error about account not found, got %v", err)
	}

	template := `
		port: -1
		accounts: {
			ACC1: { users = [{user: "user1", password: "user1"}] }
			ACC2: { users = [{user: "user2", password: "user2"}] }
		}
		leafnodes: {
			port: -1
			authorization {
			  %s
              account: "ACC1"
            }
		}
	`
	for iter, test := range []struct {
		name       string
		userSpec   string
		lnURLCreds string
		shouldFail bool
	}{
		{"no user creds required and no user so binds to ACC1", "", "", false},
		{"no user creds required and pick user2 associated to ACC2", "", "user2:user2@", false},
		{"no user creds required and unknown user should fail", "", "unknown:user@", true},
		{"user creds required so binds to ACC1", "user: \"ln\"\npass: \"pwd\"", "ln:pwd@", false},
	} {
		t.Run(test.name, func(t *testing.T) {

			conf := createConfFile(t, []byte(fmt.Sprintf(template, test.userSpec)))
			defer os.Remove(conf)
			s1, o1 := RunServerWithConfig(conf)
			defer s1.Shutdown()

			// Create a sub on "foo" for account ACC1 (user user1), which is the one
			// bound to the accepted LN connection.
			ncACC1 := natsConnect(t, fmt.Sprintf("nats://user1:user1@%s:%d", o1.Host, o1.Port))
			defer ncACC1.Close()
			sub1 := natsSubSync(t, ncACC1, "foo")
			natsFlush(t, ncACC1)

			// Create a sub on "foo" for account ACC2 (user user2). This one should
			// not receive any message.
			ncACC2 := natsConnect(t, fmt.Sprintf("nats://user2:user2@%s:%d", o1.Host, o1.Port))
			defer ncACC2.Close()
			sub2 := natsSubSync(t, ncACC2, "foo")
			natsFlush(t, ncACC2)

			conf = createConfFile(t, []byte(fmt.Sprintf(`
				port: -1
				leafnodes: {
					remotes = [ { url: "nats-leaf://%s%s:%d" } ]
				}
			`, test.lnURLCreds, o1.LeafNode.Host, o1.LeafNode.Port)))
			defer os.Remove(conf)
			s2, _ := RunServerWithConfig(conf)
			defer s2.Shutdown()

			if test.shouldFail {
				// Wait a bit and ensure that there is no leaf node connection
				time.Sleep(100 * time.Millisecond)
				checkFor(t, time.Second, 15*time.Millisecond, func() error {
					if n := s1.NumLeafNodes(); n != 0 {
						return fmt.Errorf("Expected no leafnode connection, got %v", n)
					}
					return nil
				})
				return
			}

			checkLeafNodeConnected(t, s2)

			nc := natsConnect(t, s2.ClientURL())
			defer nc.Close()
			natsPub(t, nc, "foo", []byte("hello"))
			// If url contains known user, even when there is no credentials
			// required, the connection will be bound to the user's account.
			if iter == 1 {
				// Should not receive on "ACC1", but should on "ACC2"
				if _, err := sub1.NextMsg(100 * time.Millisecond); err != nats.ErrTimeout {
					t.Fatalf("Expected timeout error, got %v", err)
				}
				natsNexMsg(t, sub2, time.Second)
			} else {
				// Should receive on "ACC1"...
				natsNexMsg(t, sub1, time.Second)
				// but not received on "ACC2" since leafnode bound to account "ACC1".
				if _, err := sub2.NextMsg(100 * time.Millisecond); err != nats.ErrTimeout {
					t.Fatalf("Expected timeout error, got %v", err)
				}
			}
		})
	}
}

func TestLeafNodeBasicAuthMultiple(t *testing.T) {
	conf := createConfFile(t, []byte(`
		port: -1
		accounts: {
			S1ACC1: { users = [{user: "user1", password: "user1"}] }
			S1ACC2: { users = [{user: "user2", password: "user2"}] }
		}
		leafnodes: {
			port: -1
			authorization {
			  users = [
				  {user: "ln1", password: "ln1", account: "S1ACC1"}
				  {user: "ln2", password: "ln2", account: "S1ACC2"}
			  ]
            }
		}
	`))
	defer os.Remove(conf)
	s1, o1 := RunServerWithConfig(conf)
	defer s1.Shutdown()

	// Make sure that we reject a LN connection if user does not match
	conf = createConfFile(t, []byte(fmt.Sprintf(`
		port: -1
		leafnodes: {
			remotes = [{url: "nats-leaf://wron:user@%s:%d"}]
		}
	`, o1.LeafNode.Host, o1.LeafNode.Port)))
	defer os.Remove(conf)
	s2, _ := RunServerWithConfig(conf)
	defer s2.Shutdown()
	// Give a chance for s2 to attempt to connect and make sure that s1
	// did not register a LN connection.
	time.Sleep(100 * time.Millisecond)
	if n := s1.NumLeafNodes(); n != 0 {
		t.Fatalf("Expected no leafnode connection, got %v", n)
	}
	s2.Shutdown()

	ncACC1 := natsConnect(t, fmt.Sprintf("nats://user1:user1@%s:%d", o1.Host, o1.Port))
	defer ncACC1.Close()
	sub1 := natsSubSync(t, ncACC1, "foo")
	natsFlush(t, ncACC1)

	ncACC2 := natsConnect(t, fmt.Sprintf("nats://user2:user2@%s:%d", o1.Host, o1.Port))
	defer ncACC2.Close()
	sub2 := natsSubSync(t, ncACC2, "foo")
	natsFlush(t, ncACC2)

	// We will start s2 with 2 LN connections that should bind local account S2ACC1
	// to account S1ACC1 and S2ACC2 to account S1ACC2 on s1.
	conf = createConfFile(t, []byte(fmt.Sprintf(`
		port: -1
		accounts {
			S2ACC1 { users = [{user: "user1", password: "user1"}] }
			S2ACC2 { users = [{user: "user2", password: "user2"}] }
		}
		leafnodes: {
			remotes = [
				{
					url: "nats-leaf://ln1:ln1@%s:%d"
					account: "S2ACC1"
				}
				{
					url: "nats-leaf://ln2:ln2@%s:%d"
					account: "S2ACC2"
				}
			]
		}
	`, o1.LeafNode.Host, o1.LeafNode.Port, o1.LeafNode.Host, o1.LeafNode.Port)))
	defer os.Remove(conf)
	s2, o2 := RunServerWithConfig(conf)
	defer s2.Shutdown()

	checkFor(t, 5*time.Second, 100*time.Millisecond, func() error {
		if nln := s2.NumLeafNodes(); nln != 2 {
			return fmt.Errorf("Expected 2 connected leafnodes for server %q, got %d", s2.ID(), nln)
		}
		return nil
	})

	// Create a user connection on s2 that binds to S2ACC1 (use user1).
	nc1 := natsConnect(t, fmt.Sprintf("nats://user1:user1@%s:%d", o2.Host, o2.Port))
	defer nc1.Close()

	// Create an user connection on s2 that binds to S2ACC2 (use user2).
	nc2 := natsConnect(t, fmt.Sprintf("nats://user2:user2@%s:%d", o2.Host, o2.Port))
	defer nc2.Close()

	// Now if a message is published from nc1, sub1 should receive it since
	// their account are bound together.
	natsPub(t, nc1, "foo", []byte("hello"))
	natsNexMsg(t, sub1, time.Second)
	// But sub2 should not receive it since different account.
	if _, err := sub2.NextMsg(100 * time.Millisecond); err != nats.ErrTimeout {
		t.Fatalf("Expected timeout error, got %v", err)
	}

	// Now use nc2 (S2ACC2) to publish
	natsPub(t, nc2, "foo", []byte("hello"))
	// Expect sub2 to receive and sub1 not to.
	natsNexMsg(t, sub2, time.Second)
	if _, err := sub1.NextMsg(100 * time.Millisecond); err != nats.ErrTimeout {
		t.Fatalf("Expected timeout error, got %v", err)
	}
}

func TestLeafNodeAccountsMap(t *testing.T) {
	opts := DefaultOptions()
	opts.Accounts = []*Account{NewAccount("A"), NewAccount("B")}
	opts.LeafNode.Port = -1
	opts.LeafNode.Users = []*User{
		&User{Username: "userA", Password: "pwd", Account: opts.Accounts[0]},
		&User{Username: "userB", Password: "pwd", Account: opts.Accounts[1]},
	}
	s := RunServer(opts)
	defer s.Shutdown()

	connectLeaf := func(t *testing.T, user, accName string) *Server {
		t.Helper()
		u, _ := url.Parse(fmt.Sprintf("nats://%s:pwd@127.0.0.1:%d", user, opts.LeafNode.Port))
		o := DefaultOptions()
		o.Accounts = []*Account{NewAccount(accName)}
		o.LeafNode.Remotes = []*RemoteLeafOpts{
			{
				LocalAccount: accName,
				URLs:         []*url.URL{u},
			},
		}
		s := RunServer(o)

		checkLeafNodeConnected(t, s)
		return s
	}

	ln1 := connectLeaf(t, "userA", "A")
	defer ln1.Shutdown()

	checkRefs := func(t *testing.T, s *Server, accName string, expectedExist, expectedRebuild bool, expectedRefs int) {
		t.Helper()
		lna := &s.leafNodeAccs
		lna.mu.Lock()
		refs, ok := lna.m[accName]
		needsRebuild := lna.needsRebuild
		lna.mu.Unlock()
		if !expectedExist && ok {
			t.Fatalf("Account %q should not be on the map, but it was", accName)
		} else if expectedExist && !ok {
			t.Fatalf("Account %q should have been in the map, but it wasn't", accName)
		}
		if refs != expectedRefs {
			t.Fatalf("Expected ref count for account %q to be %v, got %v", accName, expectedRefs, refs)
		}
		if needsRebuild != expectedRebuild {
			t.Fatalf("Expected rebuild bool for account %q to be %v, got %v", accName, expectedRebuild, needsRebuild)
		}
		accs, _, _ := s.getLeafNodesAccountNames()
		for _, a := range accs {
			if a == accName {
				if expectedExist {
					return
				}
				t.Fatalf("Account %q should not have been in the array %+v", accName, accs)
			}
		}
	}
	checkRefs(t, s, "A", true, true, 1)
	// We don't stop in the soliciting server...
	checkRefs(t, ln1, "A", false, false, 0)

	ln2 := connectLeaf(t, "userA", "A")
	defer ln2.Shutdown()

	checkRefs(t, s, "A", true, false, 2)
	checkRefs(t, ln1, "A", false, false, 0)
	checkRefs(t, ln2, "A", false, false, 0)

	disconnectLeaf := func(t *testing.T, s, ln *Server, expectedLeft int) {
		t.Helper()
		ln.Shutdown()
		checkFor(t, time.Second, 15*time.Millisecond, func() error {
			if n := s.NumLeafNodes(); n != expectedLeft {
				return fmt.Errorf("Expected %d leaf node(s), got %v", expectedLeft, n)
			}
			return nil
		})
	}
	disconnectLeaf(t, s, ln2, 1)

	checkRefs(t, s, "A", true, false, 1)
	checkRefs(t, ln1, "A", false, false, 0)

	// Create a new one on different account
	ln3 := connectLeaf(t, "userB", "B")
	defer ln3.Shutdown()

	checkRefs(t, s, "B", true, true, 1)
	checkRefs(t, ln1, "A", false, false, 0)
	checkRefs(t, ln1, "B", false, false, 0)
	checkRefs(t, ln3, "A", false, false, 0)
	checkRefs(t, ln3, "B", false, false, 0)

	// Shutdown ln1, which should remove "A" from map
	disconnectLeaf(t, s, ln1, 1)
	checkRefs(t, s, "A", false, true, 0)
	checkRefs(t, ln3, "A", false, false, 0)
	checkRefs(t, ln3, "B", false, false, 0)

	// Finally ln3..
	disconnectLeaf(t, s, ln3, 0)
	checkRefs(t, s, "B", false, true, 0)
}

func TestLeafNodeWithGateways(t *testing.T) {
	oa := testDefaultOptionsForGateway("A")
	oa.Accounts = []*Account{NewAccount("SYS")}
	oa.SystemAccount = "SYS"
	oa.LeafNode.Port = -1
	sa := RunServer(oa)
	defer sa.Shutdown()

	ob := testGatewayOptionsFromToWithServers(t, "B", "A", sa)
	ob.Accounts = []*Account{NewAccount("SYS")}
	ob.SystemAccount = "SYS"
	sb := RunServer(ob)
	defer sb.Shutdown()

	waitForOutboundGateways(t, sa, 1, time.Second)
	waitForOutboundGateways(t, sb, 1, time.Second)
	waitForInboundGateways(t, sa, 1, time.Second)
	waitForInboundGateways(t, sb, 1, time.Second)

	setRecSubExp := func(s *Server) {
		s.mu.Lock()
		s.gateway.recSubExp = 10 * time.Millisecond
		s.mu.Unlock()
	}
	setRecSubExp(sb)

	lso := DefaultOptions()
	u, _ := url.Parse(fmt.Sprintf("nats://%s:%d", oa.LeafNode.Host, oa.LeafNode.Port))
	lso.LeafNode.Remotes = []*RemoteLeafOpts{{URLs: []*url.URL{u}}}
	ls := RunServer(lso)
	defer ls.Shutdown()

	// Create a responder on ls
	ncResp := natsConnect(t, ls.ClientURL())
	defer ncResp.Close()
	respSub := natsSubSync(t, ncResp, "foo")

	checkReqReply := func(t *testing.T) {
		t.Helper()

		// Create a connection for requestor on B
		ncReq := natsConnect(t, sb.ClientURL())
		defer ncReq.Close()

		// Setup sub for response manually
		subReply := natsSubSync(t, ncReq, "bar")
		// Wait for more than reqSubExp so that the reply subject
		// is not mapped.
		time.Sleep(100 * time.Millisecond)

		// Send request
		ncReq.PublishRequest("foo", "bar", []byte("request"))

		// Responder should receive it.
		m := natsNexMsg(t, respSub, time.Second)
		// Check reply is not mapped...
		if m.Reply != "bar" {
			t.Fatalf("Expected reply to be \"bar\", got %q", m.Reply)
		}
		// Now send reply
		m.Respond([]byte("reply"))

		// Requestor's sub should receive it.
		natsNexMsg(t, subReply, time.Second)
	}
	checkReqReply(t)

	// Now restart server B...
	sb.Shutdown()
	sb = RunServer(ob)
	defer sb.Shutdown()

	// Again, set recent sub expiration to low value so we test
	// when reply subject is not mapped.
	setRecSubExp(sb)

	// Check again...
	checkReqReply(t)
}
