XPI = mailreaper.xpi
VERSION = $(shell grep '"version"' manifest.json | head -1 | sed 's/.*: *"//;s/".*//')

SOURCES = manifest.json background.js \
	$(shell find _locales actions icons llm options popup rules -type f)

EXCLUDE = --exclude '.*' --exclude '*/.DS_Store' --exclude 'Makefile' --exclude 'CLAUDE.md' --exclude 'README.md' --exclude 'REVIEW_NOTES.md' --exclude 'PRIVACY.md' --exclude '*.xpi'

.PHONY: all clean

all: $(XPI)

$(XPI): $(SOURCES)
	@rm -f $@
	zip -r $@ . $(EXCLUDE)
	@echo "Built $(XPI) (v$(VERSION), $$(du -h $@ | cut -f1))"

clean:
	rm -f $(XPI)
