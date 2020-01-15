package database

// This file was adapted from https://github.com/bombsimon/gorm-bulk
import (
	"fmt"
	"github.com/pkg/errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jinzhu/gorm"
)

// bulkNow holds a global now state and will be used for each records as
// CreatedAt and UpdatedAt value if they're empty. This value will be set to the
// value from gorm.NowFunc() in scopeFromObjects to ensure all objects get the
// same value.
// nolint: gochecknoglobals
var bulkNow time.Time

func BulkInsert(db *gorm.DB, objects interface{}) error {
	return bulkExec(db, objects)
}

// BulkInsertChunk will split the objects passed into the passed chunk size. A
// slice of errors will be returned (if any).
func BulkInsertChunk(db *gorm.DB, objects interface{}, chunkSize int) []error {
	var allErrors []error

	if reflect.TypeOf(objects).Kind() != reflect.Slice {
		return []error{errors.New("objects arg is not a slice")}
	}

	// Reflect the objects array into generic value
	v := reflect.ValueOf(objects)

	for {
		var chunkObjects interface{}

		if v.Len() <= chunkSize {
			chunkObjects = v.Interface()
			v = reflect.ValueOf([]interface{}{})
		} else {
			chunkObjects = v.Slice(0, chunkSize).Interface()
			// TODO: Check whether -1 should not be here
			v = v.Slice(chunkSize, v.Len())
		}

		if err := bulkExec(db, chunkObjects); err != nil {
			allErrors = append(allErrors, err)
		}

		// Nothing more to do
		if v.Len() < 1 {
			break
		}
	}

	if len(allErrors) > 0 {
		return allErrors
	}

	return nil
}

// bulkExec will convert a slice of interfaces to bulk SQL statement.
func bulkExec(db *gorm.DB, objects interface{}) error {
	scope, err := scopeFromObjects(db, objects)
	if err != nil {
		return err
	}

	// No scope and no error means nothing to do
	if scope == nil {
		return nil
	}

	return db.Exec(scope.SQL, scope.SQLVars...).Error
}

func scopeFromObjects(db *gorm.DB, objects interface{}) (*gorm.Scope, error) {
	if reflect.TypeOf(objects).Kind() != reflect.Slice {
		return nil, errors.New("objects arg is not a slice")
	}
	// Reflect the objects array into generic value
	v := reflect.ValueOf(objects)
	//elemType := reflect.TypeOf(objects).Elem()

	if v.Len() < 1 {
		return nil, nil
	}
	// Retrieve 0th element as an interface
	firstElem := v.Index(0).Interface()

	var (
		scope = db.NewScope(firstElem)
	)

	// Ensure we set the correct time and reset it after we're done.
	bulkNow = gorm.NowFunc()

	defer func() {
		bulkNow = time.Time{}
	}()

	// Get a map of the first element to calculate field names and number of
	// placeholders.
	firstObjectFields, err := objectToMap(firstElem)
	if err != nil {
		return nil, err
	}

	columnNames, placeholders := buildColumnsAndPlaceholders(firstObjectFields)

	// We must setup quotedColumnNames after sorting columnNames since sorting
	// of quoted fields might differ from sorting without. This way we know that
	// columnNames is the master of the order and will be used both when setting
	// field and values order.
	quotedColumnNames := make([]string, len(columnNames))
	for i := range columnNames {
		quotedColumnNames[i] = scope.Quote(gorm.ToColumnName(columnNames[i]))
	}

	groups := make([]string, 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		// Retrieve ith element as an interface
		r := v.Index(i).Interface()
		objectScope := db.NewScope(r)

		row, err := objectToMap(r)
		if err != nil {
			return nil, err
		}

		for _, key := range columnNames {
			objectScope.AddToVars(row[key])
		}

		groups[i] = fmt.Sprintf("(%s)", strings.Join(placeholders, ", "))

		// Add object vars to the outer scope vars
		scope.SQLVars = append(scope.SQLVars, objectScope.SQLVars...)
	}

	insertFunc(scope, quotedColumnNames, groups)

	return scope, nil
}

func buildColumnsAndPlaceholders(firstObjectFields map[string]interface{}) ([]string, []string) {
	columnNames := make([]string, 0, len(firstObjectFields))
	placeholders := make([]string, 0, len(firstObjectFields))
	for k := range firstObjectFields {
		// Add raw column names to use for iteration over each row later to get
		// the correct order of columns.
		columnNames = append(columnNames, k)

		// Add as many placeholders (question marks) as there are columns.
		placeholders = append(placeholders, "?")

		// Sort the column names to ensure the right order.
		sort.Strings(columnNames)
	}
	return columnNames, placeholders
}

func insertFunc(scope *gorm.Scope, columnNames, groups []string) {
	var (
		extraOptions string
	)

	if insertOption, ok := scope.Get("gorm:insert_option"); ok {
		// Add the extra insert option
		extraOptions = fmt.Sprintf(" %s", insertOption)
	}

	scope.Raw(fmt.Sprintf( //nolint:gosec
		"INSERT INTO %s (%s) VALUES %s %s",
		scope.QuotedTableName(),
		strings.Join(columnNames, ", "),
		strings.Join(groups, ", "),
		extraOptions,


	))

}

// objectToMap takes any object of type <T> and returns a map with the gorm
// field DB name as key and the value as value
func objectToMap(object interface{}) (map[string]interface{}, error) {
	var attributes = map[string]interface{}{}
	var now = bulkNow

	// De-reference pointers (and it's values)
	rv := reflect.ValueOf(object)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
		object = rv.Interface()
	}

	if rv.Kind() != reflect.Struct {
		return nil, errors.New("value must be kind of Struct")
	}

	if now.IsZero() {
		now = gorm.NowFunc()
	}

	for _, field := range (&gorm.Scope{Value: object}).Fields() {
		// Exclude relational record because it's not directly contained in database columns
		_, hasForeignKey := field.TagSettingsGet("FOREIGNKEY")
		if hasForeignKey {
			continue
		}

		if field.StructField.Relationship != nil {
			continue
		}

		if field.IsIgnored {
			continue
		}

		// Let the DBM set the default values since these might be meta values such as 'CURRENT_TIMESTAMP'. Has default
		// will be set to true also for 'AUTO_INCREMENT' fields which is not primary keys so we must check that we've
		// ACTUALLY configured a default value and uses the tag before we skip it.
		if field.StructField.HasDefaultValue && field.IsBlank {
			if _, ok := field.TagSettingsGet("DEFAULT"); ok {
				continue
			}
		}

		// Skip blank primary key fields named ID. They're probably coming from
		// `gorm.Model` which doesn't have the AUTO_INCREMENT tag.
		if field.DBName == "id" && field.IsPrimaryKey && field.IsBlank {
			continue
		}

		if field.Struct.Name == "CreatedAt" || field.Struct.Name == "UpdatedAt" {
			if field.IsBlank {
				attributes[field.DBName] = now
				continue
			}
		}

		attributes[field.DBName] = field.Field.Interface()
	}

	return attributes, nil
}
