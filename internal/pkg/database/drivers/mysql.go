package drivers

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/alessandrolattao/sqlai/internal/pkg/database"
	_ "github.com/go-sql-driver/mysql" // Import the MySQL driver
)

// MySQLAdapter implements the Adapter interface for MySQL
type MySQLAdapter struct{}

// Connect establishes a connection to a MySQL database
func (a *MySQLAdapter) Connect(config database.Config) (*sql.DB, error) {
	var connStr string
	
	if config.ConnectionString != "" {
		connStr = config.ConnectionString
	} else {
		// MySQL connection string: username:password@tcp(host:port)/dbname
		connStr = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s",
			config.User, config.Password, config.Host, config.Port, config.DBName)
		
		// Add parameters like parseTime if needed
		params := []string{}
		if config.ParseTime {
			params = append(params, "parseTime=true")
		}
		
		if len(params) > 0 {
			connStr = connStr + "?" + strings.Join(params, "&")
		}
	}
	
	return sql.Open("mysql", connStr)
}

// GetTableNames retrieves all table names from a MySQL database
func (a *MySQLAdapter) GetTableNames(db *sql.DB) ([]string, error) {
	query := "SHOW TABLES"
	
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, err
		}
		tables = append(tables, tableName)
	}

	return tables, nil
}

// GetTableDefinition retrieves the definition of a specific MySQL table
func (a *MySQLAdapter) GetTableDefinition(db *sql.DB, tableName string) (*database.TableDefinition, error) {
	// Get columns
	columnsQuery := `
		SELECT 
			COLUMN_NAME, 
			DATA_TYPE, 
			IS_NULLABLE, 
			COLUMN_DEFAULT,
			COLUMN_KEY = 'PRI' AS is_primary,
			EXTRA = 'auto_increment' AS is_autoincrement
		FROM 
			INFORMATION_SCHEMA.COLUMNS 
		WHERE 
			TABLE_SCHEMA = DATABASE() AND 
			TABLE_NAME = ?
		ORDER BY 
			ORDINAL_POSITION
	`

	rows, err := db.Query(columnsQuery, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []database.ColumnDefinition
	for rows.Next() {
		var column database.ColumnDefinition
		var isNullable string
		var defaultValue sql.NullString
		var isPrimary bool
		var isAutoIncr bool

		if err := rows.Scan(
			&column.Name, 
			&column.Type, 
			&isNullable, 
			&defaultValue, 
			&isPrimary, 
			&isAutoIncr,
		); err != nil {
			return nil, err
		}

		column.Nullable = isNullable == "YES"
		if defaultValue.Valid {
			column.Default = defaultValue.String
		}
		column.IsPrimary = isPrimary
		column.IsAutoIncr = isAutoIncr

		columns = append(columns, column)
	}

	// Get constraints (foreign keys)
	constraintsQuery := `
		SELECT
			CONSTRAINT_NAME,
			CONSTRAINT_TYPE,
			'' AS constraint_definition,
			REFERENCED_TABLE_NAME
		FROM
			INFORMATION_SCHEMA.TABLE_CONSTRAINTS
		WHERE
			TABLE_SCHEMA = DATABASE() AND
			TABLE_NAME = ? AND
			CONSTRAINT_TYPE != 'CHECK'
		ORDER BY
			CONSTRAINT_TYPE
	`

	constraintRows, err := db.Query(constraintsQuery, tableName)
	if err != nil {
		return nil, err
	}
	defer constraintRows.Close()

	var constraints []database.ConstraintDefinition
	for constraintRows.Next() {
		var constraint database.ConstraintDefinition
		if err := constraintRows.Scan(
			&constraint.Name,
			&constraint.Type,
			&constraint.Definition,
			&constraint.ReferencedTable,
		); err != nil {
			return nil, err
		}
		
		// Get constraint definition for foreign keys
		if constraint.Type == "FOREIGN KEY" {
			keyQuery := `
				SELECT
					COLUMN_NAME,
					REFERENCED_COLUMN_NAME
				FROM
					INFORMATION_SCHEMA.KEY_COLUMN_USAGE
				WHERE
					TABLE_SCHEMA = DATABASE() AND
					TABLE_NAME = ? AND
					CONSTRAINT_NAME = ?
			`
			
			keyRows, err := db.Query(keyQuery, tableName, constraint.Name)
			if err != nil {
				return nil, err
			}
			defer keyRows.Close()
			
			var columnNames []string
			var refColumnNames []string
			
			for keyRows.Next() {
				var columnName, refColumnName string
				if err := keyRows.Scan(&columnName, &refColumnName); err != nil {
					return nil, err
				}
				
				columnNames = append(columnNames, columnName)
				refColumnNames = append(refColumnNames, refColumnName)
			}
			
			constraint.Definition = fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s)",
				strings.Join(columnNames, ", "),
				constraint.ReferencedTable,
				strings.Join(refColumnNames, ", "))
				
			constraint.ReferencedColumns = refColumnNames
		}
		
		constraints = append(constraints, constraint)
	}

	return &database.TableDefinition{
		Name:        tableName,
		Columns:     columns,
		Constraints: constraints,
	}, nil
}

// GetDatabaseSchema retrieves schema information for all MySQL tables
func (a *MySQLAdapter) GetDatabaseSchema(db *sql.DB) (string, error) {
	tables, err := a.GetTableNames(db)
	if err != nil {
		return "", err
	}

	var schemaBuilder strings.Builder
	schemaBuilder.WriteString("DATABASE SCHEMA:\n\n")

	for _, tableName := range tables {
		tableDef, err := a.GetTableDefinition(db, tableName)
		if err != nil {
			return "", err
		}

		schemaBuilder.WriteString(fmt.Sprintf("TABLE: %s\n", tableDef.Name))

		// Columns
		schemaBuilder.WriteString("Columns:\n")
		for _, col := range tableDef.Columns {
			nullable := "NOT NULL"
			if col.Nullable {
				nullable = "NULL"
			}

			defaultVal := ""
			if col.Default != "" {
				defaultVal = fmt.Sprintf(" DEFAULT %s", col.Default)
			}

			primaryKey := ""
			if col.IsPrimary {
				primaryKey = " PRIMARY KEY"
			}

			autoIncr := ""
			if col.IsAutoIncr {
				autoIncr = " AUTO_INCREMENT"
			}

			schemaBuilder.WriteString(fmt.Sprintf("  %s %s %s%s%s%s\n",
				col.Name, col.Type, nullable, defaultVal, primaryKey, autoIncr))
		}

		// Constraints
		if len(tableDef.Constraints) > 0 {
			schemaBuilder.WriteString("Constraints:\n")
			for _, constraint := range tableDef.Constraints {
				schemaBuilder.WriteString(fmt.Sprintf("  %s: %s\n",
					constraint.Type, constraint.Definition))

				if constraint.Type == "FOREIGN KEY" && constraint.ReferencedTable != "" {
					schemaBuilder.WriteString(fmt.Sprintf("    REFERENCES: %s\n",
						constraint.ReferencedTable))
				}
			}
		}

		schemaBuilder.WriteString("\n")
	}

	return schemaBuilder.String(), nil
}