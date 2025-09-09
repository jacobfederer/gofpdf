package gofpdf

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Attachment defines a content to be included in the pdf, in one
// of the following ways :
//   - associated with the document as a whole : see SetAttachments()
//   - accessible via a link localized on a page : see AddAttachmentAnnotation()
type Attachment struct {
	Content []byte

	// Filename is the displayed name of the attachment
	Filename string

	// Mimetype indicates what file type is embedded
	Mimetype string

	// Description is only displayed when using AddAttachmentAnnotation(),
	// and might be modified by the pdf reader.
	Description string

	// Relationship indicates to readers or processors of the file, what relationship
	//the embedded file has to visual representation (e.g. supporting or replacing (alternative), please see enums for types)
	Relationship Relationship

	// ModificationTime indicates when the embedded file was created or embedded
	ModificationTime time.Time

	objectNumber int // filled when content is included
}

type Relationship string

const (
	RelationshipUnknown     Relationship = ""
	RelationshipData        Relationship = "Data"
	RelationshipSource      Relationship = "Source"
	RelationshipAlternative Relationship = "Alternative"
	RelationshipSupplement  Relationship = "Supplement"
)

// return the hex encoded checksum of `data`
func checksum(data []byte) string {
	tmp := md5.Sum(data)
	sl := make([]byte, len(tmp))
	for i, v := range tmp {
		sl[i] = v
	}
	return hex.EncodeToString(sl)
}

// Writes a compressed file like object as “/EmbeddedFile“. Compressing is
// done with deflate. Includes length, compressed length, MD5 checksum and optional the mimetype and the modification time.
func (f *Fpdf) writeCompressedFileObject(content []byte, mimeType string, modTime time.Time) {
	lenUncompressed := len(content)
	sum := checksum(content)
	compressed := sliceCompress(content)
	lenCompressed := len(compressed)
	f.newobj()

	var modTimeString string

	if !modTime.IsZero() {
		modTimeString = fmt.Sprintf("D:%s", modTime.UTC().Format("20060102150405"))
	}

	var formattedMimeType string

	if mimeType != "" {
		formattedMimeType = fmt.Sprintf("/%s", strings.ReplaceAll(mimeType, "/", "#2F"))
	}

	f.outf("<< /Type /EmbeddedFile /Subtype %s /Length %d /Filter /FlateDecode /Params << /CheckSum <%s> /Size %d /ModDate %s >> >>\n",
		formattedMimeType,
		lenCompressed,
		sum, lenUncompressed, f.textstring(modTimeString))
	f.putstream(compressed)
	f.out("endobj")
}

// Embed includes the content of `a`, and update its internal reference.
func (f *Fpdf) embed(a *Attachment) {
	if a.objectNumber != 0 { // already embedded (objectNumber start at 2)
		return
	}
	oldState := f.state
	f.state = 1 // we write file content in the main buffer
	f.writeCompressedFileObject(a.Content, a.Mimetype, a.ModificationTime)
	streamID := f.n
	f.newobj()

	var relationshipString string

	if a.Relationship != RelationshipUnknown {
		relationshipString = fmt.Sprintf("/%s", string(a.Relationship))
	}

	f.outf("<< /Type /Filespec /F () /UF %s /EF << /F %d 0 R >> /AFRelationship %s /Desc %s  \n>>",
		f.textstring(utf8toutf16(a.Filename)),
		streamID,
		relationshipString,
		f.textstring(utf8toutf16(a.Description)),
	)
	f.out("endobj")
	a.objectNumber = f.n
	f.state = oldState
}

// SetAttachments writes attachments as embedded files (document attachment).
// These attachments are global, see AddAttachmentAnnotation() for a link
// anchored in a page. Note that only the last call of SetAttachments is
// useful, previous calls are discarded. Be aware that not all PDF readers
// support document attachments. See the SetAttachment example for a
// demonstration of this method.
func (f *Fpdf) SetAttachments(as []Attachment) {
	f.attachments = as
}

// embed current attachments. store object numbers
// for later use by getEmbeddedFiles()
func (f *Fpdf) putAttachments() {
	for i, a := range f.attachments {
		f.embed(&a)
		f.attachments[i] = a
	}
}

// return /EmbeddedFiles tree name catalog entry.
func (f Fpdf) getEmbeddedFiles() string {
	names := make([]string, len(f.attachments))
	for i, as := range f.attachments {
		names[i] = fmt.Sprintf("(Attachement%d) %d 0 R ", i+1, as.objectNumber)
	}
	nameTree := fmt.Sprintf("<< /Names [\n %s \n] >>", strings.Join(names, "\n"))
	return nameTree
}

// Return a space-separated list of object references (e.g., "12 0 R 15 0 R")
// for all document-level attachments that declare an AFRelationship.
// This is intended to be used in the Catalog's /AF array.
func (f Fpdf) getAssociatedFilesArray() string {
	refs := make([]string, 0, len(f.attachments))
	for _, as := range f.attachments {
		if as.objectNumber == 0 {
			// not embedded yet; skip
			continue
		}
		if as.Relationship == RelationshipUnknown {
			continue
		}
		refs = append(refs, fmt.Sprintf("%d 0 R", as.objectNumber))
	}
	return strings.Join(refs, " ")
}

// ---------------------------------- Annotations ----------------------------------

type annotationAttach struct {
	*Attachment

	x, y, w, h float64 // fpdf coordinates (y diff and scaling done)
}

// AddAttachmentAnnotation puts a link on the current page, on the rectangle
// defined by `x`, `y`, `w`, `h`. This link points towards the content defined
// in `a`, which is embedded in the document. Note than no drawing is done by
// this method : a method like `Cell()` or `Rect()` should be called to
// indicate to the reader that there is a link here. Requiring a pointer to an
// Attachment avoids useless copies in the resulting pdf: attachment pointing
// to the same data will have their content only be included once, and be
// shared amongst all links. Be aware that not all PDF readers support
// annotated attachments. See the AddAttachmentAnnotation example for a
// demonstration of this method.
func (f *Fpdf) AddAttachmentAnnotation(a *Attachment, x, y, w, h float64) {
	if a == nil {
		return
	}
	f.pageAttachments[f.page] = append(f.pageAttachments[f.page], annotationAttach{
		Attachment: a,
		x:          x * f.k, y: f.hPt - y*f.k, w: w * f.k, h: h * f.k,
	})
}

// embed current annotations attachments. store object numbers
// for later use by putAttachmentAnnotationLinks(), which is
// called for each page.
func (f *Fpdf) putAnnotationsAttachments() {
	// avoid duplication
	m := map[*Attachment]bool{}
	for _, l := range f.pageAttachments {
		for _, an := range l {
			if m[an.Attachment] { // already embedded
				continue
			}
			f.embed(an.Attachment)
		}
	}
}

func (f *Fpdf) putAttachmentAnnotationLinks(out *fmtBuffer, page int) {
	for _, an := range f.pageAttachments[page] {
		// Rearrange points in the correct order
		x1 := an.x
		yTop := an.y
		x2 := an.x + an.w
		y1 := yTop - an.h

		// Make sure that Rect [llx lly urx ury] is in a ascending order
		ury := yTop
		lly := y1
		if lly > ury {
			lly, ury = ury, lly
		}

		out.printf("<< /Type /Annot /Subtype /FileAttachment /Rect [%.2f %.2f %.2f %.2f] /Border [0 0 0]\n",
			x1, lly, x2, ury)
		out.printf("/Contents %s ", f.textstring(utf8toutf16(an.Description)))
		out.printf("/T %s ", f.textstring(utf8toutf16(an.Filename)))
		out.printf("/Name /PushPin ")
		out.printf("/FS %d 0 R ", an.objectNumber)
		// If a relationship is set, also add an AF array that points to the Filespec
		if an.Relationship != RelationshipUnknown {
			out.printf("/AF [%d 0 R] ", an.objectNumber)
		}
		out.printf(">>\n")

	}
}
