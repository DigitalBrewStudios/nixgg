CC ?= cc
CFLAGS ?= -O2 -Wall
# Caller sets CODEGEN=/nix/store/…/codegen — path to the tool built
# by phase 1. Phase 2's build execs it to produce generated.h,
# which main.c then #includes.
CODEGEN ?= $(error CODEGEN must point at the phase-1 tool)

app: main.o
	$(CC) main.o -o app

main.o: main.c generated.h
	$(CC) $(CFLAGS) -c main.c -o main.o

generated.h:
	$(CODEGEN) hello > generated.h

clean:
	rm -f main.o app generated.h

.PHONY: clean
