// database.go
//
// things for database

package main

import (
	"fmt"
	"log"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Prompt struct
type Prompt struct {
	gorm.Model

	ChatID   int64 `gorm:"index"`
	UserID   int64
	Username string

	Text   string
	Tokens uint `gorm:"index"`

	Result Generated
}

// Generated struct
type Generated struct {
	gorm.Model

	Successful bool `gorm:"index"`
	Text       string
	Tokens     uint `gorm:"index"`

	PromptID int64 // foreign key
}

// Database struct
type Database struct {
	db *gorm.DB
}

// open and return a database at given path: `dbPath`.
func openDatabase(dbPath string) (database *Database, err error) {
	var db *gorm.DB
	db, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		PrepareStmt: true,
	})

	if err == nil {
		// migrate tables
		if err := db.AutoMigrate(
			&Prompt{},
			&Generated{},
		); err != nil {
			log.Printf("failed to migrate databases: %s", err)
		}

		return &Database{db: db}, nil
	}

	return nil, err
}

// save `prompt`.
func (d *Database) savePrompt(prompt Prompt) (err error) {
	tx := d.db.Save(&prompt)
	return tx.Error
}

// save `prompt` and its result to logs database
func savePromptAndResult(db *Database, chatID, userID int64, username string, prompt string, promptTokens uint, result string, resultTokens uint, resultSuccessful bool) {
	if db != nil {
		if err := db.savePrompt(Prompt{
			ChatID:   chatID,
			UserID:   userID,
			Username: username,
			Text:     prompt,
			Tokens:   promptTokens,
			Result: Generated{
				Successful: resultSuccessful,
				Text:       result,
				Tokens:     resultTokens,
			},
		}); err != nil {
			log.Printf("failed to save prompt & result to database: %s", err)
		}
	}
}

const (
	numSuccessfulPromptsToLoad = 5
)

// load recent `prompt`s and their results.
func (d *Database) loadSuccessfulPrompts(userID int64) (result []Prompt, err error) {
	tx := d.db.Model(&Prompt{}).
		Preload("Result").
		Joins("JOIN generateds ON generateds.prompt_id = prompts.id").
		Where("prompts.user_id = ? AND generateds.successful = ?", userID, true).
		Order("prompts.created_at DESC").
		Limit(numSuccessfulPromptsToLoad).
		Find(&result)

	return result, tx.Error
}

// retrieve successful prompts and their results
func retrieveSuccessfulPrompts(db *Database, userID int64) (result []Prompt) {
	result = []Prompt{}

	if db != nil {
		var err error
		if result, err = db.loadSuccessfulPrompts(userID); err != nil {
			log.Printf("failed to load prompts from database: %s", err)
		}
	}

	return result
}

// retrieve stats from database
func retrieveStats(db *Database) string {
	if db == nil {
		return msgDatabaseNotConfigured
	} else {
		lines := []string{}

		var prompt Prompt
		if tx := db.db.First(&prompt); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("Since %s", prompt.CreatedAt.Format("2006-01-02 15:04:05")))
			lines = append(lines, "")
		}

		printer := message.NewPrinter(language.English) // for adding commas to numbers

		var count int64
		if tx := db.db.Table("prompts").Select("count(distinct chat_id) as count").Scan(&count); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("Chats: %s", printer.Sprintf("%d", count)))
		}

		var sumAndCount struct {
			Sum   int64
			Count int64
		}
		if tx := db.db.Table("prompts").Select("sum(tokens) as sum, count(id) as count").Where("tokens > 0").Scan(&sumAndCount); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("Prompts: %s (Total tokens: %s)", printer.Sprintf("%d", sumAndCount.Count), printer.Sprintf("%d", sumAndCount.Sum)))
		}
		if tx := db.db.Table("generateds").Select("sum(tokens) as sum, count(id) as count").Where("successful = 1").Scan(&sumAndCount); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("Completions: %s (Total tokens: %s)", printer.Sprintf("%d", sumAndCount.Count), printer.Sprintf("%d", sumAndCount.Sum)))
		}
		if tx := db.db.Table("generateds").Select("count(id) as count").Where("successful = 0").Scan(&count); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("Errors: %s", printer.Sprintf("%d", count)))
		}

		if len(lines) > 0 {
			return strings.Join(lines, "\n")
		}

		return msgDatabaseEmpty
	}
}
