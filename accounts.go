package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Account holds credentials for one Zed account.
type Account struct {
	Name           string
	UserID         string
	CredentialJSON string // raw JSON of the credential object

	mu       sync.Mutex
	jwtToken string
	jwtExp   int64
}

// AccountManager holds all accounts and tracks which is current.
type AccountManager struct {
	mu       sync.RWMutex
	accounts []*Account
	current  int
}

func newAccountManager() *AccountManager {
	return &AccountManager{}
}

// accountsFilePath returns the path to accounts.json,
// honouring the ACCOUNTS_FILE environment variable.
func accountsFilePath() string {
	if p := os.Getenv("ACCOUNTS_FILE"); p != "" {
		return p
	}
	return "accounts.json"
}

// loadFromFile reads accounts.json into the manager.
func (m *AccountManager) loadFromFile() error {
	data, err := os.ReadFile(accountsFilePath())
	if err != nil {
		return err
	}

	var root struct {
		Accounts map[string]struct {
			UserID     any             `json:"user_id"`
			Credential json.RawMessage `json:"credential"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse accounts.json: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.accounts = nil
	m.current = 0

	for name, v := range root.Accounts {
		uid := ""
		switch u := v.UserID.(type) {
		case string:
			uid = u
		case float64:
			uid = fmt.Sprintf("%.0f", u)
		}
		if uid == "" || len(v.Credential) == 0 {
			continue
		}
		m.accounts = append(m.accounts, &Account{
			Name:           name,
			UserID:         uid,
			CredentialJSON: string(v.Credential),
		})
	}
	return nil
}

// saveToFile persists current accounts list back to accounts.json.
func (m *AccountManager) saveToFile() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	type entry struct {
		UserID     string          `json:"user_id"`
		Credential json.RawMessage `json:"credential"`
	}
	root := map[string]map[string]entry{
		"accounts": {},
	}
	for _, acc := range m.accounts {
		root["accounts"][acc.Name] = entry{
			UserID:     acc.UserID,
			Credential: json.RawMessage(acc.CredentialJSON),
		}
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(accountsFilePath(), data, 0600)
}

// listJSON returns JSON for the /zed/accounts endpoint.
func (m *AccountManager) listJSON() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	type accInfo struct {
		Name    string `json:"name"`
		UserID  string `json:"user_id"`
		Current bool   `json:"current"`
	}
	var list []accInfo
	currentName := ""
	for i, acc := range m.accounts {
		isCurrent := i == m.current
		list = append(list, accInfo{Name: acc.Name, UserID: acc.UserID, Current: isCurrent})
		if isCurrent {
			currentName = acc.Name
		}
	}
	return json.Marshal(map[string]any{
		"accounts": list,
		"current":  currentName,
	})
}

// switchTo sets the current account by name; returns false if not found.
func (m *AccountManager) switchTo(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, acc := range m.accounts {
		if acc.Name == name {
			m.current = i
			return true
		}
	}
	return false
}

// getCurrent returns the current account (nil if none).
func (m *AccountManager) getCurrent() *Account {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.accounts) == 0 {
		return nil
	}
	return m.accounts[m.current]
}

// getOrderedAccounts returns accounts starting from current, for failover.
func (m *AccountManager) getOrderedAccounts() []*Account {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := len(m.accounts)
	if n == 0 {
		return nil
	}
	out := make([]*Account, 0, n)
	out = append(out, m.accounts[m.current])
	for i, acc := range m.accounts {
		if i != m.current {
			out = append(out, acc)
		}
	}
	return out
}

// failover switches to the given account index after a successful stream.
func (m *AccountManager) failover(acc *Account) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, a := range m.accounts {
		if a == acc {
			m.current = i
			return
		}
	}
}
