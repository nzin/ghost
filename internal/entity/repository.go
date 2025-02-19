package entity

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Alayacare/goliac/internal/utils"
	"github.com/go-git/go-billy/v5"
	"gopkg.in/yaml.v3"
)

type Repository struct {
	Entity `yaml:",inline"`
	Spec   struct {
		Writers             []string            `yaml:"writers,omitempty"`
		Readers             []string            `yaml:"readers,omitempty"`
		ExternalUserReaders []string            `yaml:"externalUserReaders,omitempty"`
		ExternalUserWriters []string            `yaml:"externalUserWriters,omitempty"`
		IsPublic            bool                `yaml:"public,omitempty"`
		AllowAutoMerge      bool                `yaml:"allow_auto_merge,omitempty"`
		DeleteBranchOnMerge bool                `yaml:"delete_branch_on_merge,omitempty"`
		AllowUpdateBranch   bool                `yaml:"allow_update_branch,omitempty"`
		Rulesets            []RepositoryRuleSet `yaml:"rulesets,omitempty"`
	} `yaml:"spec,omitempty"`
	Archived      bool    `yaml:"archived,omitempty"` // implicit: will be set by Goliac
	Owner         *string `yaml:"-"`                  // implicit. team name owning the repo (if any)
	RenameTo      string  `yaml:"renameTo,omitempty"`
	DirectoryPath string  `yaml:"-"` // used to know where to rename the repository
}

type RepositoryRuleSet struct {
	RuleSetDefinition `yaml:",inline"`
	Name              string `yaml:"name"`
}

/*
 * NewRepository reads a file and returns a Repository object
 * The next step is to validate the Repository object using the Validate method
 */
func NewRepository(fs billy.Filesystem, filename string) (*Repository, error) {
	filecontent, err := utils.ReadFile(fs, filename)
	if err != nil {
		return nil, err
	}

	repository := &Repository{}
	err = yaml.Unmarshal(filecontent, repository)
	if err != nil {
		return nil, err
	}
	repository.DirectoryPath = filepath.Dir(filename)

	return repository, nil
}

/**
 * ReadRepositories reads all the files in the dirname directory and
 * add them to the owner's team and returns
 * - a map of Repository objects
 * - a slice of errors that must stop the validation process
 * - a slice of warning that must not stop the validation process
 */
func ReadRepositories(fs billy.Filesystem, archivedDirname string, teamDirname string, teams map[string]*Team, externalUsers map[string]*User) (map[string]*Repository, []error, []Warning) {
	errors := []error{}
	warning := []Warning{}
	repos := make(map[string]*Repository)

	// archived dir
	exist, err := utils.Exists(fs, archivedDirname)
	if err != nil {
		errors = append(errors, err)
		return repos, errors, warning
	}
	if exist {
		entries, err := fs.ReadDir(archivedDirname)
		if err != nil {
			errors = append(errors, err)
			return nil, errors, warning
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			// skipping files starting with '.'
			if entry.Name()[0] == '.' {
				continue
			}
			if !strings.HasSuffix(entry.Name(), ".yaml") {
				warning = append(warning, fmt.Errorf("file %s doesn't have a .yaml extension", entry.Name()))
				continue
			}
			repo, err := NewRepository(fs, filepath.Join(archivedDirname, entry.Name()))
			if err != nil {
				errors = append(errors, err)
			} else {
				if err := repo.Validate(filepath.Join(archivedDirname, entry.Name()), teams, externalUsers); err != nil {
					errors = append(errors, err)
				} else {
					repo.Archived = true
					repos[repo.Name] = repo
				}
			}
		}
	}
	// regular teams dir
	exist, err = utils.Exists(fs, teamDirname)
	if err != nil {
		errors = append(errors, err)
		return repos, errors, warning
	}
	if !exist {
		return repos, errors, warning
	}

	// Parse all the repositories in the teamDirname directory
	entries, err := fs.ReadDir(teamDirname)
	if err != nil {
		errors = append(errors, err)
		return nil, errors, warning
	}

	for _, team := range entries {
		if team.IsDir() {
			suberrs, subwarns := recursiveReadRepositories(fs, archivedDirname, filepath.Join(teamDirname, team.Name()), team.Name(), repos, teams, externalUsers)
			errors = append(errors, suberrs...)
			warning = append(warning, subwarns...)
		}
	}

	return repos, errors, warning
}

func recursiveReadRepositories(fs billy.Filesystem, archivedDirPath string, teamDirPath string, teamName string, repos map[string]*Repository, teams map[string]*Team, externalUsers map[string]*User) ([]error, []Warning) {
	errors := []error{}
	warnings := []Warning{}

	subentries, err := fs.ReadDir(teamDirPath)
	if err != nil {
		errors = append(errors, err)
		return errors, warnings
	}
	for _, sube := range subentries {
		if sube.IsDir() && sube.Name()[0] != '.' {
			suberrs, subwarns := recursiveReadRepositories(fs, archivedDirPath, filepath.Join(teamDirPath, sube.Name()), sube.Name(), repos, teams, externalUsers)
			errors = append(errors, suberrs...)
			warnings = append(warnings, subwarns...)
		}
		if !sube.IsDir() && filepath.Ext(sube.Name()) == ".yaml" && sube.Name() != "team.yaml" {
			repo, err := NewRepository(fs, filepath.Join(teamDirPath, sube.Name()))
			if err != nil {
				errors = append(errors, err)
			} else {
				if err := repo.Validate(filepath.Join(teamDirPath, sube.Name()), teams, externalUsers); err != nil {
					errors = append(errors, err)
				} else {
					// check if the repository doesn't already exists
					if _, exist := repos[repo.Name]; exist {
						existing := filepath.Join(archivedDirPath, repo.Name)
						if repos[repo.Name].Owner != nil {
							existing = filepath.Join(*repos[repo.Name].Owner, repo.Name)
						}
						errors = append(errors, fmt.Errorf("Repository %s defined in 2 places (check %s and %s)", repo.Name, filepath.Join(teamDirPath, sube.Name()), existing))
					} else {
						teamname := teamName
						repo.Owner = &teamname
						repo.Archived = false
						repos[repo.Name] = repo
					}
				}
			}
		}
	}
	return errors, warnings
}

func (r *Repository) Validate(filename string, teams map[string]*Team, externalUsers map[string]*User) error {

	if r.ApiVersion != "v1" {
		return fmt.Errorf("invalid apiVersion: %s (check repository filename %s)", r.ApiVersion, filename)
	}

	if r.Kind != "Repository" {
		return fmt.Errorf("invalid kind: %s (check repository filename %s)", r.Kind, filename)
	}

	if r.Name == "" {
		return fmt.Errorf("name is empty (check repository filename %s)", filename)
	}

	filename = filepath.Base(filename)
	if r.Name != filename[:len(filename)-len(filepath.Ext(filename))] {
		return fmt.Errorf("invalid name: %s for repository filename %s", r.Name, filename)
	}

	for _, writer := range r.Spec.Writers {
		if _, ok := teams[writer]; !ok {
			return fmt.Errorf("invalid writer: %s doesn't exist (check repository filename %s)", writer, filename)
		}
	}
	for _, reader := range r.Spec.Readers {
		if _, ok := teams[reader]; !ok {
			return fmt.Errorf("invalid reader: %s doesn't exist (check repository filename %s)", reader, filename)
		}
	}

	for _, externalUserReader := range r.Spec.ExternalUserReaders {
		if _, ok := externalUsers[externalUserReader]; !ok {
			return fmt.Errorf("invalid externalUserReader: %s doesn't exist in repository filename %s", externalUserReader, filename)
		}
	}

	for _, externalUserWriter := range r.Spec.ExternalUserWriters {
		if _, ok := externalUsers[externalUserWriter]; !ok {
			return fmt.Errorf("invalid externalUserWriter: %s doesn't exist in repository filename %s", externalUserWriter, filename)
		}
	}

	rulesetname := make(map[string]bool)
	for _, ruleset := range r.Spec.Rulesets {
		if ruleset.Name == "" {
			return fmt.Errorf("invalid ruleset: each ruleset must have a name")
		}
		if ruleset.Enforcement != "disable" && ruleset.Enforcement != "active" && ruleset.Enforcement != "evaluate" {
			return fmt.Errorf("invalid ruleset %s enforcement: it must be 'disable','active' or 'evaluate'", ruleset.Name)
		}
		if _, ok := rulesetname[ruleset.Name]; ok {
			return fmt.Errorf("invalid ruleset: each ruleset must have a uniq name, found 2 times %s", ruleset.Name)
		}
		rulesetname[ruleset.Name] = true
	}

	if utils.GithubAnsiString(r.Name) != r.Name {
		return fmt.Errorf("invalid name: %s will be changed to %s (check repository filename %s)", r.Name, utils.GithubAnsiString(r.Name), filename)
	}

	return nil
}
