// +build tensorflow

package hercules

import (
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/gogo/protobuf/proto"
	"gopkg.in/bblfsh/sdk.v1/uast"
	progress "gopkg.in/cheggaaa/pb.v1"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/hercules.v3/pb"
	"gopkg.in/vmarkovtsev/BiDiSentiment.v1"
)

// CommentSentimentAnalysis measures comment sentiment through time.
type CommentSentimentAnalysis struct {
	MinCommentLength int
	Gap              float32

	commentsByDay map[int][]string
	commitsByDay  map[int][]plumbing.Hash
	xpather       *ChangesXPather
}

// CommentSentimentResult contains the sentiment values per day, where 1 means very negative
// and 0 means very positive.
type CommentSentimentResult struct {
	EmotionsByDay map[int]float32
	CommentsByDay map[int][]string
	commitsByDay  map[int][]plumbing.Hash
}

const (
	ConfigCommentSentimentMinLength = "CommentSentiment.MinLength"
	ConfigCommentSentimentGap       = "CommentSentiment.Gap"

	DefaultCommentSentimentCommentMinLength = 20
	DefaultCommentSentimentGap              = float32(0.5)

	// CommentLettersRatio is the threshold to filter impure comments which contain code.
	CommentLettersRatio = 0.6
)

var (
	filteredFirstCharRE = regexp.MustCompile("[^a-zA-Z0-9]")
	filteredCharsRE     = regexp.MustCompile("[^-a-zA-Z0-9_:;,./?!#&%+*=\\n \\t()]+")
	charsRE             = regexp.MustCompile("[a-zA-Z]+")
	functionNameRE      = regexp.MustCompile("\\s*[a-zA-Z_][a-zA-Z_0-9]*\\(\\)")
	whitespaceRE        = regexp.MustCompile("\\s+")
	licenseRE           = regexp.MustCompile("(?i)[li[cs]en[cs][ei]|copyright|©")
)

// Name of this PipelineItem. Uniquely identifies the type, used for mapping keys, etc.
func (sent *CommentSentimentAnalysis) Name() string {
	return "Sentiment"
}

// Provides returns the list of names of entities which are produced by this PipelineItem.
// Each produced entity will be inserted into `deps` of dependent Consume()-s according
// to this list. Also used by hercules.Registry to build the global map of providers.
func (sent *CommentSentimentAnalysis) Provides() []string {
	return []string{}
}

// Requires returns the list of names of entities which are needed by this PipelineItem.
// Each requested entity will be inserted into `deps` of Consume(). In turn, those
// entities are Provides() upstream.
func (sent *CommentSentimentAnalysis) Requires() []string {
	arr := [...]string{DependencyUastChanges, DependencyDay}
	return arr[:]
}

// Features which must be enabled for this PipelineItem to be automatically inserted into the DAG.
func (sent *CommentSentimentAnalysis) Features() []string {
	arr := [...]string{FeatureUast}
	return arr[:]
}

// ListConfigurationOptions returns the list of changeable public properties of this PipelineItem.
func (sent *CommentSentimentAnalysis) ListConfigurationOptions() []ConfigurationOption {
	options := [...]ConfigurationOption{{
		Name:        ConfigCommentSentimentMinLength,
		Description: "Minimum length of the comment to be analyzed.",
		Flag:        "min-comment-len",
		Type:        IntConfigurationOption,
		Default:     DefaultCommentSentimentCommentMinLength}, {
		Name: ConfigCommentSentimentGap,
		Description: "Sentiment value threshold, values between 0.5 - X/2 and 0.5 + x/2 will not be " +
			"considered. Must be >= 0 and < 1. The purpose is to exclude neutral comments.",
		Flag:    "sentiment-gap",
		Type:    FloatConfigurationOption,
		Default: DefaultCommentSentimentGap},
	}
	return options[:]
}

// Flag returns the command line switch which activates the analysis.
func (sent *CommentSentimentAnalysis) Flag() string {
	return "sentiment"
}

// Configure sets the properties previously published by ListConfigurationOptions().
func (sent *CommentSentimentAnalysis) Configure(facts map[string]interface{}) {
	if val, exists := facts[ConfigCommentSentimentGap]; exists {
		sent.Gap = val.(float32)
	}
	if val, exists := facts[ConfigCommentSentimentMinLength]; exists {
		sent.MinCommentLength = val.(int)
	}
	sent.validate()
	sent.commitsByDay = facts[FactCommitsByDay].(map[int][]plumbing.Hash)
}

func (sent *CommentSentimentAnalysis) validate() {
	if sent.Gap < 0 || sent.Gap >= 1 {
		log.Printf("Sentiment gap is too big: %f => reset to the default %f",
			sent.Gap, DefaultCommentSentimentGap)
		sent.Gap = DefaultCommentSentimentGap
	}
	if sent.MinCommentLength < 10 {
		log.Printf("Comment minimum length is too small: %d => reset to the default %d",
			sent.MinCommentLength, DefaultCommentSentimentCommentMinLength)
		sent.MinCommentLength = DefaultCommentSentimentCommentMinLength
	}
}

// Initialize resets the temporary caches and prepares this PipelineItem for a series of Consume()
// calls. The repository which is going to be analysed is supplied as an argument.
func (sent *CommentSentimentAnalysis) Initialize(repository *git.Repository) {
	sent.commentsByDay = map[int][]string{}
	sent.xpather = &ChangesXPather{XPath: "//*[@roleComment]"}
	sent.validate()
}

// Consume runs this PipelineItem on the next commit data.
// `deps` contain all the results from upstream PipelineItem-s as requested by Requires().
// Additionally, "commit" is always present there and represents the analysed *object.Commit.
// This function returns the mapping with analysis results. The keys must be the same as
// in Provides(). If there was an error, nil is returned.
func (sent *CommentSentimentAnalysis) Consume(deps map[string]interface{}) (map[string]interface{}, error) {
	changes := deps[DependencyUastChanges].([]UASTChange)
	day := deps[DependencyDay].(int)
	commentNodes := sent.xpather.Extract(changes)
	comments := sent.mergeComments(commentNodes)
	dayComments := sent.commentsByDay[day]
	if dayComments == nil {
		dayComments = []string{}
	}
	dayComments = append(dayComments, comments...)
	sent.commentsByDay[day] = dayComments
	return nil, nil
}

// Finalize returns the result of the analysis. Further Consume() calls are not expected.
func (sent *CommentSentimentAnalysis) Finalize() interface{} {
	result := CommentSentimentResult{
		EmotionsByDay: map[int]float32{},
		CommentsByDay: map[int][]string{},
		commitsByDay:  sent.commitsByDay,
	}
	days := make([]int, 0, len(sent.commentsByDay))
	for day := range sent.commentsByDay {
		days = append(days, day)
	}
	sort.Ints(days)
	texts := []string{}
	for _, key := range days {
		texts = append(texts, sent.commentsByDay[key]...)
	}
	session, err := sentiment.OpenSession()
	if err != nil {
		panic(err)
	}
	defer session.Close()
	var bar *progress.ProgressBar
	callback := func(pos int, total int) {
		if bar == nil {
			bar = progress.New(total)
			bar.Callback = func(msg string) {
				os.Stderr.WriteString("\r" + msg)
			}
			bar.NotPrint = true
			bar.ShowPercent = false
			bar.ShowSpeed = false
			bar.SetMaxWidth(80)
			bar.Start()
		}
		bar.Set(pos)
	}
	// we run the bulk evaluation in the end for efficiency
	weights, err := sentiment.EvaluateWithProgress(texts, session, callback)
	if bar != nil {
		bar.Finish()
	}
	if err != nil {
		panic(err)
	}
	pos := 0
	for _, key := range days {
		sum := float32(0)
		comments := make([]string, 0, len(sent.commentsByDay[key]))
		for _, comment := range sent.commentsByDay[key] {
			if weights[pos] < 0.5*(1-sent.Gap) || weights[pos] > 0.5*(1+sent.Gap) {
				sum += weights[pos]
				comments = append(comments, comment)
			}
			pos++
		}
		if len(comments) > 0 {
			result.EmotionsByDay[key] = sum / float32(len(comments))
			result.CommentsByDay[key] = comments
		}
	}
	return result
}

// Serialize converts the analysis result as returned by Finalize() to text or bytes.
// The text format is YAML and the bytes format is Protocol Buffers.
func (sent *CommentSentimentAnalysis) Serialize(result interface{}, binary bool, writer io.Writer) error {
	sentimentResult := result.(CommentSentimentResult)
	if binary {
		return sent.serializeBinary(&sentimentResult, writer)
	}
	sent.serializeText(&sentimentResult, writer)
	return nil
}

func (sent *CommentSentimentAnalysis) serializeText(result *CommentSentimentResult, writer io.Writer) {
	days := make([]int, 0, len(result.EmotionsByDay))
	for day := range result.EmotionsByDay {
		days = append(days, day)
	}
	sort.Ints(days)
	for _, day := range days {
		commits := result.commitsByDay[day]
		hashes := make([]string, len(commits))
		for i, hash := range commits {
			hashes[i] = hash.String()
		}
		fmt.Fprintf(writer, "  %d: [%.4f, [%s], \"%s\"]\n",
			day, result.EmotionsByDay[day], strings.Join(hashes, ","),
			strings.Join(result.CommentsByDay[day], "|"))
	}
}

func (sent *CommentSentimentAnalysis) serializeBinary(
	result *CommentSentimentResult, writer io.Writer) error {
	message := pb.CommentSentimentResults{
		SentimentByDay: map[int32]*pb.Sentiment{},
	}
	for key, val := range result.EmotionsByDay {
		commits := make([]string, len(result.commitsByDay[key]))
		for i, commit := range result.commitsByDay[key] {
			commits[i] = commit.String()
		}
		message.SentimentByDay[int32(key)] = &pb.Sentiment{
			Value:    val,
			Comments: result.CommentsByDay[key],
			Commits:  commits,
		}
	}
	serialized, err := proto.Marshal(&message)
	if err != nil {
		return err
	}
	writer.Write(serialized)
	return nil
}

func (sent *CommentSentimentAnalysis) mergeComments(nodes []*uast.Node) []string {
	mergedComments := []string{}
	lines := map[int][]*uast.Node{}
	for _, node := range nodes {
		if node.StartPosition == nil {
			continue
		}
		lineno := int(node.StartPosition.Line)
		subnodes := lines[lineno]
		if subnodes == nil {
			subnodes = []*uast.Node{}
		}
		subnodes = append(subnodes, node)
		lines[lineno] = subnodes
	}
	lineNums := make([]int, 0, len(lines))
	for line := range lines {
		lineNums = append(lineNums, line)
	}
	sort.Ints(lineNums)
	buffer := []string{}
	for i, line := range lineNums {
		lineNodes := lines[line]
		maxEnd := line
		for _, node := range lineNodes {
			if node.EndPosition != nil && maxEnd < int(node.EndPosition.Line) {
				maxEnd = int(node.EndPosition.Line)
			}
			token := strings.TrimSpace(node.Token)
			if token != "" {
				buffer = append(buffer, token)
			}
		}
		if i < len(lineNums)-1 && lineNums[i+1] <= maxEnd+1 {
			continue
		}
		mergedComments = append(mergedComments, strings.Join(buffer, "\n"))
		buffer = buffer[:0]
	}
	// We remove unneeded chars and filter too short comments
	filteredComments := make([]string, 0, len(mergedComments))
	for _, comment := range mergedComments {
		comment = strings.TrimSpace(comment)
		if comment == "" || filteredFirstCharRE.MatchString(comment[:1]) {
			// heuristic - we discard docstrings
			continue
		}
		// heuristic - remove function names
		comment = functionNameRE.ReplaceAllString(comment, "")
		comment = filteredCharsRE.ReplaceAllString(comment, "")
		if len(comment) < sent.MinCommentLength {
			continue
		}
		// collapse whitespace
		comment = whitespaceRE.ReplaceAllString(comment, " ")
		// heuristic - number of letters must be at least 60%
		charsCount := 0
		for _, match := range charsRE.FindAllStringIndex(comment, -1) {
			charsCount += match[1] - match[0]
		}
		if charsCount < int(float32(len(comment))*CommentLettersRatio) {
			continue
		}
		// heuristic - license
		if licenseRE.MatchString(comment) {
			continue
		}
		filteredComments = append(filteredComments, comment)
	}
	return filteredComments
}

func init() {
	Registry.Register(&CommentSentimentAnalysis{})
}