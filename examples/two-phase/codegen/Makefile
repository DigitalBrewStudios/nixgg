CC ?= cc
CFLAGS ?= -O2 -Wall

codegen: codegen.o
	$(CC) codegen.o -o codegen

%.o: %.c
	$(CC) $(CFLAGS) -c $< -o $@

clean:
	rm -f codegen.o codegen

.PHONY: clean
