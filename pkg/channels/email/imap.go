package email

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"slices"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/sipeed/picoclaw/pkg/logger"
)

func (c *EmailChannel) fetchNewMessages() ([]incomingMessage, uint32, error) {
	client, err := c.dialIMAP()
	if err != nil {
		return nil, 0, err
	}
	defer client.Close()

	if err := client.Login(c.config.Username, c.config.Password).Wait(); err != nil {
		return nil, 0, fmt.Errorf("imap login failed: %w", err)
	}
	if _, err := client.Select(c.config.Folder, nil).Wait(); err != nil {
		return nil, 0, fmt.Errorf("imap select failed: %w", err)
	}

	searchData, err := client.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		return nil, 0, fmt.Errorf("imap search failed: %w", err)
	}
	allUIDs := searchData.AllUIDs()
	maxMessageUID := maxUID(allUIDs)

	c.mu.Lock()
	if !c.state.Initialized {
		c.state.Initialized = true
		c.state.LastSeenUID = maxMessageUID
		err := c.saveStateLocked()
		c.mu.Unlock()
		if err != nil {
			return nil, 0, err
		}
		logger.InfoCF("email", "Email channel baseline initialized", map[string]any{
			"last_seen_uid": maxMessageUID,
		})
		return nil, maxMessageUID, nil
	}
	lastSeenUID := c.state.LastSeenUID
	c.mu.Unlock()

	var freshUIDs []imap.UID
	for _, uid := range allUIDs {
		if uint32(uid) <= lastSeenUID {
			continue
		}
		freshUIDs = append(freshUIDs, uid)
	}
	if len(freshUIDs) == 0 {
		return nil, maxMessageUID, nil
	}

	slices.Sort(freshUIDs)
	var messages []incomingMessage
	for _, uid := range freshUIDs {
		msg, err := c.fetchMessageByUID(client, uid)
		if err != nil {
			logger.WarnCF("email", "Failed to fetch email", map[string]any{
				"uid":   uid,
				"error": err.Error(),
			})
			continue
		}
		if c.shouldIgnoreMessage(msg) {
			continue
		}
		messages = append(messages, msg)
	}

	return messages, maxMessageUID, nil
}

func (c *EmailChannel) fetchMessageByUID(client *imapclient.Client, uid imap.UID) (incomingMessage, error) {
	bodySection := &imap.FetchItemBodySection{}
	fetchCmd := client.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
		UID:         true,
		Envelope:    true,
		BodySection: []*imap.FetchItemBodySection{bodySection},
	})
	defer fetchCmd.Close()

	fetched := fetchCmd.Next()
	if fetched == nil {
		if err := fetchCmd.Close(); err != nil {
			return incomingMessage{}, err
		}
		return incomingMessage{}, fmt.Errorf("uid %d not found", uid)
	}

	var raw bytes.Buffer
	for {
		item := fetched.Next()
		if item == nil {
			break
		}
		if bodyData, ok := item.(imapclient.FetchItemDataBodySection); ok {
			if _, err := io.Copy(&raw, bodyData.Literal); err != nil {
				return incomingMessage{}, err
			}
		}
	}
	if err := fetchCmd.Close(); err != nil {
		return incomingMessage{}, err
	}
	if raw.Len() == 0 {
		return incomingMessage{}, fmt.Errorf("uid %d returned empty body", uid)
	}

	return parseIncomingMessage(uint32(uid), raw.Bytes())
}

func (c *EmailChannel) markIMAPAnswered(uid uint32) error {
	client, err := c.dialIMAP()
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Login(c.config.Username, c.config.Password).Wait(); err != nil {
		return err
	}
	if _, err := client.Select(c.config.Folder, nil).Wait(); err != nil {
		return err
	}

	storeFlags := &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Flags:  []imap.Flag{imap.FlagSeen, imap.FlagAnswered},
		Silent: true,
	}
	return client.Store(imap.UIDSetNum(imap.UID(uid)), storeFlags, nil).Close()
}

func (c *EmailChannel) dialIMAP() (*imapclient.Client, error) {
	addr := fmt.Sprintf("%s:%d", c.config.IMAPHost, c.config.IMAPPort)
	if c.config.IMAPTLS {
		return imapclient.DialTLS(addr, &imapclient.Options{
			TLSConfig: &tls.Config{
				ServerName: c.config.IMAPHost,
				MinVersion: tls.VersionTLS12,
			},
		})
	}
	return imapclient.DialStartTLS(addr, &imapclient.Options{
		TLSConfig: &tls.Config{
			ServerName: c.config.IMAPHost,
			MinVersion: tls.VersionTLS12,
		},
	})
}
