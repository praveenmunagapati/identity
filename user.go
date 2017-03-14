package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// le user
type User struct {
	Id             string   `json:"id" sql:"id"`
	Created        int64    `json:"created" sql:"created"`
	Updated        int64    `json:"updated" sql:"updated"`
	Username       string   `json:"username" sql:"username"`
	Type           UserType `json:"type" sql:"type"`
	password       string
	Email          string `json:"email" sql:"email"`
	Name           string `json:"name" sql:"name"`
	Description    string `json:"description" sql:"description"`
	HomeUrl        string `json:"home_url" sql:"home_url"`
	emailConfirmed bool   `sql:"email_confirmed"`
	isAdmin        bool   `sql:"is_admin"`
	accessToken    string `sql:"access_token"`
	anonymous      bool
}

// create a new user struct pointer from a provided id string
func NewUser(id string) *User {
	return &User{Id: id, Type: UserTypeUser}
}

// NewUserFromFromString attempts to place the provided string in the right field.
// id if it's a valid uuid, username if it's a valid username, or throwing away the
// string if none of the above apply
func NewUserFromString(s string) *User {
	if validUuid(s) {
		return &User{Id: s}
	} else if validUsername(s) {
		return &User{Username: s}
	}

	return &User{}
}

func userColumns() string {
	return "id, created, updated, username, type, name, description, home_url, email, email_confirmed, is_admin"
}

// _user is a private struct for marshaling & unmarshaling
type _user User

// MarshalJSON is a custom JSON implementation that delivers a uuid-string if the
// model is blank, or an object otherwise
func (u User) MarshalJSON() ([]byte, error) {
	// if we only have the Id of the user, but not created & updated
	// values, there's a very good chance this value hasn't been properly
	// read from the db, so let's return just an id string instead
	if u.Created == 0 && u.Updated == 0 && u.Id != "" {
		return []byte(fmt.Sprintf(`"%s"`, u.Id)), nil
	}

	return json.Marshal(_user(u))
}

// UnmarshalJSON is a custom json implementation that supports a few different inputs
// if a string is provided, it first checks if the string is a valid uuid, if so it'll set
// the string to the id. If not it'll check to see if the passed-in string is a valid username
// and if so it'll set the user's username accordingly.
// if an object is passed in we skip straight to regular json unmarshalling
func (u *User) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*u = *NewUserFromString(s)
		return nil
	}

	user := _user{}
	if err := json.Unmarshal(data, &user); err != nil {
		return err
	}

	*u = User(user)
	return nil
}

// just the username
func (u *User) Slug() string {
	return u.Username
}

// return url endpoint path to user. basically: /:username
func (u *User) Path() string {
	return fmt.Sprintf("/%s", u.Username)
}

// load the given user from the database based on
// id, username, or email
func (u *User) Read(db *sql.DB) error {
	var clause, value string

	if u.Id != "" {
		clause = "id"
		value = u.Id
	} else if u.Username != "" {
		clause = "username"
		value = u.Username
	} else if u.Email != "" {
		clause = "email"
		value = u.Email
	} else if u.accessToken != "" {
		clause = "access_token"
		value = u.accessToken
	} else {
		return ErrNotFound
	}

	row := db.QueryRow(fmt.Sprintf("SELECT %s FROM users WHERE %s= $1 AND deleted=false", userColumns(), clause), value)
	user := &User{}
	err := user.UnmarshalSQL(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		} else {
			return New500Error(err.Error())
		}
	}

	*u = *user
	return nil
}

func (u *User) ReadApiToken(db *sql.DB) error {
	var token string
	if err := db.QueryRow("SELECT access_token FROM users WHERE id= $1", u.Id).Scan(&token); err != nil {
		return err
	}
	u.accessToken = token
	return nil
}

func (u *User) AccessToken() string {
	return u.accessToken
}

// save a user model, creating it if it doesn't exist
// updating the user model if it doesn't
func (u *User) Save(db *sql.DB) error {
	prev := NewUser(u.Id)
	if err := prev.Read(db); err != nil {
		// create if user doesn't exist
		if err == ErrNotFound {
			if err = u.validateCreate(db); err != nil {
				return err
			}

			hash, e := bcrypt.GenerateFromPassword([]byte(u.password), bcrypt.DefaultCost)
			if e != nil {
				return Error500IfErr(e)
			}

			u.Id = NewUuid()
			u.Created = time.Now().Unix()
			u.Updated = u.Created

			token, e := NewAccessToken(db)
			if e != nil {
				return Error500IfErr(e)
			}
			u.accessToken = token

			if _, e = db.Exec("INSERT INTO users VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, false, $12, false)", u.Id, u.Created, u.Updated, u.Username, u.Type, hash, u.Email, u.Name, u.Description, u.HomeUrl, u.emailConfirmed, u.accessToken); e != nil {
				return NewFmtError(500, e.Error())
			}

			return nil
		}

		return err
	} else {
		// update the user
		if err := u.validateUpdate(db, prev); err != nil {
			return err
		}

		u.Updated = time.Now().Unix()
		tx, err := db.Begin()
		if err != nil {
			return New500Error(err.Error())
		}

		if _, err := tx.Exec("UPDATE users SET updated=$2, username= $3, type=$4, name=$5, description=$6, home_url= $7, email_confirmed=$8, access_token=$9 WHERE id= $1 AND deleted=false", u.Id, u.Updated, u.Username, u.Type, u.Name, u.Description, u.HomeUrl, u.emailConfirmed, u.accessToken); err != nil {
			tx.Rollback()
			return Error500IfErr(err)
		}

		if prev.Username != u.Username {
			// Any modifications to replicated usernames should be made here
			// TODO - permissions changes?

			// if _, err := tx.Exec("UPDATE datasets SET username= $2 WHERE username= $1", prev.Username, u.Username); err != nil {
			// 	tx.Rollback()
			// 	return Error500IfErr(err)
			// }

			// if _, err := tx.Exec("UPDATE query SET ns_user= $2 WHERE ns_user= $1", prev.Username, u.Username); err != nil {
			// 	tx.Rollback()
			// 	return Error500IfErr(err)
			// }
		}

		if err := tx.Commit(); err != nil {
			tx.Rollback()
			return Error500IfErr(err)
		}

		return Error500IfErr(err)
	}

	return nil
}

// "delete" a user
// TODO - deleting an account will require lots of cleanup:
//	* Close any open change requests
//	* Resolve any datasets that the user is the sole administrator of
func (u *User) Delete(db *sql.DB) error {
	if err := u.Read(db); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return New500Error(err.Error())
	}

	u.Updated = time.Now().Unix()
	if _, err := tx.Exec("UPDATE users SET updated= $2, deleted=true WHERE id= $1", u.Id, u.Updated); err != nil {
		tx.Rollback()
		return Error500IfErr(err)
	}

	// TODO - Users that delete their profile will need to have all their datasets deleted as well

	if err := tx.Commit(); err != nil {
		tx.Rollback()
		return Error500IfErr(err)
	}

	return nil
}

// validate a user for creation
func (u *User) validateCreate(db *sql.DB) error {
	if err := u.valFields(); err != nil {
		return err
	}

	if taken, err := UsernameTaken(db, u.Username); err != nil {
		return Error500IfErr(err)
	} else if taken {
		return ErrUsernameTaken
	}

	if taken, err := EmailTaken(db, u.Email); err != nil {
		return Error500IfErr(err)
	} else if taken {
		return ErrEmailTaken
	}

	u.password = strings.TrimSpace(u.password)
	if u.password == "" {
		return ErrPasswordRequired
	}
	if err := u.validatePassword(); err != nil {
		return err
	}

	return nil
}

// validate all common fields used for any change to the user table
func (u *User) valFields() error {
	u.Username = strings.TrimSpace(u.Username)
	if u.Username == "" {
		return ErrUsernameRequired
	}
	if !validUsername(u.Username) {
		return ErrInvalidUsername
	}

	u.Email = strings.TrimSpace(u.Email)
	if u.Email == "" {
		return ErrEmailRequired
	}
	if !validEmail(u.Email) {
		return ErrInvalidEmail
	}

	// let's not require a name
	u.Name = strings.TrimSpace(u.Name)

	return nil
}

// validate a user for updating
func (u *User) validateUpdate(db *sql.DB, prev *User) error {
	// fill in any blank data that can't be blank
	if u.Username == "" {
		u.Username = prev.Username
	}
	if u.Email == "" {
		u.Email = prev.Email
	}

	if err := u.valFields(); err != nil {
		return err
	}

	if u.Username != prev.Username {
		// logger.Println(u.Username, prev.Username)
		if taken, err := UsernameTaken(db, u.Username); err != nil {
			return err
		} else if taken {
			return ErrUsernameTaken
		}
	}

	if u.Email != prev.Email {
		if taken, err := EmailTaken(db, u.Email); err != nil {
			return err
		} else if taken {
			return ErrEmailTaken
		}
	}

	return nil
}

// create a new user from a given username, email, first, last, and password
// This is just a wrapper to turn args into a user & then call save, returning the user & error,
// But should be used to create users in case we want to inject analytics or whatever.
func CreateUser(db *sql.DB, username, email, name, password string, t UserType) (u *User, err error) {
	u = &User{
		Username:       username,
		Email:          email,
		Name:           name,
		Type:           t,
		password:       password,
		emailConfirmed: false,
	}

	err = u.Save(db)
	return
}

// attempt to authenticate a user, for now only returns either nil or ErrAccessDenied
// TODO - should also return 500-type errors when service is down
func AuthenticateUser(db *sql.DB, username, password string) (u *User, err error) {
	var hash []byte
	u = &User{Username: username}
	if err := u.Read(db); err != nil {
		return nil, ErrAccessDenied
	}

	if err := db.QueryRow("SELECT password_hash FROM users WHERE id= $1 AND deleted=false", u.Id).Scan(&hash); err != nil {
		return nil, ErrAccessDenied
	}

	if err := bcrypt.CompareHashAndPassword(hash, []byte(password)); err != nil {
		return nil, ErrAccessDenied
	}

	return u, nil
}

// confirm that a user's 'password' string field is in fact a valid password
// TODO - remove in favour of validPassword validator
func (u *User) validatePassword() error {
	u.password = strings.TrimSpace(u.password)
	if u.password == "" {
		return ErrPasswordRequired
	}
	if len(u.password) < 6 {
		return ErrPasswordTooShort
	}
	return nil
}

// private method to actually set a user's passowrd
func (u *User) savePassword(db *sql.DB) error {
	if err := u.validatePassword(); err != nil {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(u.password), bcrypt.DefaultCost)
	if err != nil {
		return Error500IfErr(err)
	}

	_, err = db.Exec("UPDATE users SET password_hash=$2 WHERE id=$1 AND deleted=false", u.Id, []byte(hash))
	return Error500IfErr(err)
}

// construct the url for a user to confirm their email address
func (u *User) confirmEmailUrl() string {
	return fmt.Sprintf("%s/email/%s/confirm", cfg.UrlRoot, u.Id)
}

// turn an sql row from the user table into a user struct pointer
func (u *User) UnmarshalSQL(row sqlScannable) error {
	var (
		id, username, name, email, description, homeUrl string
		created, updated                                int64
		emailConfirmed, isAdmin                         bool
		t                                               UserType
	)

	// "id, created, updated, username, type, name, email, email_confirmed"
	if err := row.Scan(&id, &created, &updated, &username, &t, &name, &description, &homeUrl, &email, &emailConfirmed, &isAdmin); err != nil {
		return err
	}
	*u = User{
		Id:             id,
		Created:        created,
		Updated:        updated,
		Username:       username,
		Type:           t,
		Name:           name,
		Email:          email,
		emailConfirmed: emailConfirmed,
		Description:    description,
		isAdmin:        isAdmin,
	}

	return nil
}