#include "textflag.h"

TEXT ·I386Getpid(SB),NOSPLIT|NOFRAME,$0-0
 MOVL $20, AX // i386 __NR_getpid
 INT $0x80
 RET

TEXT ·Handler(SB),NOSPLIT,$0-16
 LEAQ trap<>(SB), AX
 MOVQ AX, entry+0(FP)
 LEAQ restore<>(SB), AX
 MOVQ AX, restorer+8(FP)
 RET

// SA_SIGINFO on native Linux/amd64: SI points at siginfo_t. Preserve the
// kernel's signo, errno (the rule mark), code, syscall number and audit arch.
TEXT trap<>(SB),NOSPLIT|NOFRAME,$0
 MOVQ $1, DI
 MOVQ $32, DX
 MOVQ $1, AX // write(stdout, siginfo, 32)
 SYSCALL
 MOVQ $91, DI
 MOVQ $231, AX // exit_group
 SYSCALL
 HLT

TEXT restore<>(SB),NOSPLIT|NOFRAME,$0
 MOVQ $15, AX // rt_sigreturn
 SYSCALL
 HLT

TEXT ·ForkCall(SB),NOSPLIT|NOFRAME,$0-48
 MOVQ after+24(FP), R12
 MOVQ flag+32(FP), R13
 MOVQ number+0(FP), AX
 MOVQ arg+8(FP), DI
 MOVQ size+16(FP), SI
 XORQ DX, DX
 XORQ R10, R10
 XORQ R8, R8
 SYSCALL
 TESTQ AX, AX
 JNZ parent
 TESTQ R12,R12
 JZ exit
 MOVQ R12, AX
 MOVQ R13, DI
 SYSCALL
 NEGQ AX // return errno in wait status
exit:
 MOVQ AX, DI
 MOVQ $60, AX
 SYSCALL
 HLT
parent:
 MOVQ AX, result+40(FP)
 RET
